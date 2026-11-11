// Package setup is first-time setup: its steps in two halves, the one-time link that opens it, and the session that
// link becomes. The steps and their state live in the manager's database, so setup resumes where it stopped.
package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
)

// Half is where a step is done: over the one-time link on this machine's LAN, or over the mesh.
type Half string

const (
	LAN  Half = "lan"
	Mesh Half = "mesh"
)

// Step is one screen of first-time setup.
type Step struct {
	ID   string `json:"id"`
	Half Half   `json:"half"`
}

// Steps are first-time setup's steps, in order. The LAN half covers everything up to "mesh connected, certificate
// issued": the household, the identity provider, the mesh and its token, the domain and its DNS token, mail, and
// the recovery kit. Then the admin's device joins the mesh, the passkey is made at the sign-in's own address, and
// setup is done.
var Steps = []Step{
	{"welcome", LAN}, {"you", LAN}, {"household", LAN}, {"signin", LAN}, {"network", LAN}, {"domain", LAN},
	{"mail", LAN}, {"kit", LAN},
	{"join", Mesh}, {"passkey", Mesh}, {"done", Mesh},
}

// Handler checks a step's answer and returns what to keep of it. Answers kept here are never secret.
type Handler func(ctx context.Context, answer json.RawMessage) (map[string]string, error)

// Invalid is an answer that didn't pass its checks, naming the fields to fix and why each was refused.
type Invalid struct {
	Fields  []string
	Reasons map[string]string
}

// add refuses field for why, unless why is empty.
func (e *Invalid) add(field, why string) {
	if why == "" {
		return
	}
	if e.Reasons == nil {
		e.Reasons = map[string]string{}
	}
	e.Fields = append(e.Fields, field)
	e.Reasons[field] = why
}

func (e *Invalid) Error() string { return fmt.Sprintf("invalid answer: %v", e.Fields) }

// The ways a request can be refused.
var (
	ErrUnknownStep     = errors.New("unknown-step")
	ErrOverTheMesh     = errors.New("over-the-mesh")     // a mesh step, asked over the LAN link
	ErrEarlierStepOpen = errors.New("earlier-step-open") // steps are taken in order
	ErrNotBuilt        = errors.New("not-built")         // a step a later version of HomersLab brings
	ErrLinkRefused     = errors.New("link-refused")      // unknown, used, ended or expired
	ErrNoSession       = errors.New("no-session")
	ErrLANHalfDone     = errors.New("lan-half-done") // setup continues over the mesh
)

const (
	// LinkLife is how long a new link can be claimed.
	LinkLife = 30 * time.Minute
	// SessionIdle is how long a claimed link's session stays open without a visit.
	SessionIdle = 30 * time.Minute
	// SessionMax is how long a session lasts at most; then a new link continues where setup stopped.
	SessionMax = 2 * time.Hour
)

// Clock is setup's time.
type Clock struct {
	// Now is the wall clock: the times setup records and shows.
	Now func() time.Time
	// Elapsed is a monotonic clock, the time since a fixed moment in this process. How old a link or a session is goes
	// by it, so setting the wall clock back or forward neither lengthens nor cuts them.
	Elapsed func() time.Duration
}

// Setup is first-time setup on this primary.
type Setup struct {
	db       *store.Store
	clock    Clock
	handlers map[string]Handler

	// mu guards what this process knows of the live link and session, by its monotonic clock. A new link ends every
	// earlier one and every session, so there is at most one of each.
	mu      sync.Mutex
	link    *liveLink
	session *liveSession
}

// liveLink is the link this process made, and when (Elapsed).
type liveLink struct {
	hash []byte
	made time.Duration
}

// liveSession is the session this process opened or took over: its token's hash, the client it is bound to, when it
// was opened and last used, and when its last use was written to the database (Elapsed).
type liveSession struct {
	hash               []byte
	client             string
	opened, seen, kept time.Duration
}

// SeenEvery is how often a session's last use is written to the database, at most: reading setup writes nothing more,
// and a restarted manager knows how long the session was idle, to within this.
const SeenEvery = time.Minute

// New is first-time setup kept in db. Handlers answer the steps that are built; a step without one is not built yet.
func New(db *store.Store, clock Clock, handlers map[string]Handler) *Setup {
	return &Setup{db: db, clock: clock, handlers: handlers}
}

// StoreSteps are the steps as the database seeds them.
func StoreSteps() []store.Step {
	out := make([]store.Step, len(Steps))
	for i, s := range Steps {
		out[i] = store.Step{ID: s.ID, Half: string(s.Half), Position: i}
	}
	return out
}

// State is where setup stands: every step's state, the first one still open, and the answers so far.
type State struct {
	Half    Half              `json:"half"`
	Current string            `json:"current"`
	Steps   []StepState       `json:"steps"`
	Answers map[string]string `json:"answers"`
}

// StepState is one step and whether it is done.
type StepState struct {
	ID    string `json:"id"`
	Half  Half   `json:"half"`
	State string `json:"state"` // todo or done
}

// State reads where setup stands.
func (s *Setup) State(ctx context.Context) (State, error) {
	rows, err := s.db.Steps(ctx)
	if err != nil {
		return State{}, err
	}
	answers, err := s.db.Answers(ctx)
	if err != nil {
		return State{}, err
	}
	st := State{Steps: make([]StepState, 0, len(rows)), Answers: answers}
	for _, r := range rows {
		state := "todo"
		if r.Done {
			state = "done"
		} else if st.Current == "" {
			st.Current, st.Half = r.ID, Half(r.Half)
		}
		st.Steps = append(st.Steps, StepState{ID: r.ID, Half: Half(r.Half), State: state})
	}
	if st.Current == "" && len(rows) > 0 { // all done
		last := rows[len(rows)-1]
		st.Current, st.Half = last.ID, Half(last.Half)
	}
	return st, nil
}

// LANHalfDone says whether every step of the LAN half is done.
func (s *Setup) LANHalfDone(ctx context.Context) (bool, error) {
	st, err := s.State(ctx)
	return err == nil && st.Half == Mesh, err
}

// Answer takes a step's answer, given over the half "over", and returns the new state.
func (s *Setup) Answer(ctx context.Context, id string, over Half, answer json.RawMessage) (State, error) {
	st, err := s.State(ctx)
	if err != nil {
		return State{}, err
	}
	i := index(id)
	if i < 0 {
		return State{}, ErrUnknownStep
	}
	if Steps[i].Half != over {
		return State{}, ErrOverTheMesh
	}
	if over == LAN && st.Half == Mesh {
		return State{}, ErrLANHalfDone // the first half is settled once the mesh half has begun
	}
	for _, earlier := range st.Steps {
		if earlier.ID == id {
			break
		}
		if earlier.State != "done" {
			return State{}, ErrEarlierStepOpen
		}
	}
	h := s.handlers[id]
	if h == nil {
		return State{}, ErrNotBuilt
	}
	keep, err := h(ctx, answer)
	if err != nil {
		return State{}, err
	}
	if err := s.db.CompleteStep(ctx, id, keep, s.clock.Now()); err != nil {
		return State{}, err
	}
	return s.State(ctx)
}

func index(id string) int {
	for i, s := range Steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// Link is a new one-time link's key, which only its URL carries, and when it stops working.
type Link struct {
	Key       string
	ExpiresAt time.Time
}

// NewLink makes a one-time link, ending every earlier link and session. It is refused once the LAN half is done.
func (s *Setup) NewLink(ctx context.Context) (Link, error) {
	done, err := s.LANHalfDone(ctx)
	if err != nil {
		return Link{}, err
	}
	if done {
		return Link{}, ErrLANHalfDone
	}
	key, hash := token()
	now := s.clock.Now()
	l := Link{Key: key, ExpiresAt: now.Add(LinkLife)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.NewLink(ctx, hash, now, l.ExpiresAt); err != nil {
		return Link{}, err
	}
	s.link, s.session = &liveLink{hash: hash, made: s.clock.Elapsed()}, nil
	return l, nil
}

// Claim uses up a link and returns the token of the session it opens for client, the claiming device's address.
func (s *Setup) Claim(ctx context.Context, key, client string) (string, error) {
	hash := digest(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.db.Link(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrLinkRefused
	}
	if err != nil {
		return "", err
	}
	elapsed := s.clock.Elapsed()
	age := s.clock.Now().Sub(row.CreatedAt) // a link made before this process started
	// Whether it was claimed or revoked, ClaimLink says, at once with using it up.
	if s.link != nil && bytes.Equal(s.link.hash, hash) {
		age = elapsed - s.link.made
	}
	if !within(age, LinkLife) {
		return "", ErrLinkRefused
	}
	session, sessionHash := token()
	err = s.db.ClaimLink(ctx, hash, sessionHash, client, s.clock.Now())
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrLinkRefused
	}
	if err != nil {
		return "", err
	}
	s.session = &liveSession{hash: sessionHash, client: client, opened: elapsed, seen: elapsed, kept: elapsed}
	return session, nil
}

// Session checks a session's token, from client, and keeps the session open. The token travels in clear on the LAN,
// so it is good only from the device that claimed the link, and for a while. It writes to the database at most every
// SeenEvery, and a write that fails doesn't refuse the session: reading setup must work on a full disk.
func (s *Setup) Session(ctx context.Context, tok, client string) error {
	hash := digest(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := s.clock.Elapsed()
	live := s.session
	if live == nil || !bytes.Equal(live.hash, hash) {
		// Not this process's: a session opened before a restart carries on if the wall clock says it is within its
		// limits. Its age by the wall clock is all there is, and an age below zero is unknown.
		row, err := s.db.Session(ctx, hash)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoSession
		}
		if err != nil {
			return err
		}
		now := s.clock.Now()
		opened, idle := now.Sub(row.CreatedAt), now.Sub(row.LastSeen)
		if !within(opened, SessionMax) || !within(idle, SessionIdle) {
			return ErrNoSession
		}
		live = &liveSession{hash: hash, client: row.Client, opened: elapsed - opened, seen: elapsed - idle, kept: elapsed - idle}
		s.session = live
	}
	if live.client != client || !within(elapsed-live.opened, SessionMax) || !within(elapsed-live.seen, SessionIdle) {
		return ErrNoSession
	}
	live.seen = elapsed
	if elapsed-live.kept >= SeenEvery {
		if err := s.db.SeeSession(ctx, hash, s.clock.Now()); err == nil {
			live.kept = elapsed
		}
	}
	return nil
}

// within says whether an age is known and below limit.
func within(age, limit time.Duration) bool { return age >= 0 && age < limit }

// token is 256 random bits, URL-safe, and the hash the database keeps of it.
func token() (string, []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails: crypto/rand panics rather than return short
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, digest(t)
}

func digest(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}
