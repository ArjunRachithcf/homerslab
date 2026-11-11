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
	// Elapsed is the time since the machine booted, and Boot that boot's ID. How old a link or a session is goes by
	// them, across any number of the manager's restarts, so setting the wall clock back or forward neither lengthens
	// nor cuts them. After another boot, their age is unknown: they have ended.
	Elapsed func() time.Duration
	Boot    func() string
}

// Setup is first-time setup on this primary.
type Setup struct {
	db       *store.Store
	clock    Clock
	handlers map[string]Handler

	// mu guards the session's last use, which this process knows sooner than the database: it is written there at
	// most every SeenEvery. A new link ends every session, so there is at most one.
	mu   sync.Mutex
	seen *lastUse
}

// lastUse is when this process last saw a session used, and when it last wrote that to the database (Elapsed).
type lastUse struct {
	hash       []byte
	seen, kept time.Duration
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
	now := s.moment()
	l := Link{Key: key, ExpiresAt: now.Wall.Add(LinkLife)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.NewLink(ctx, hash, now, l.ExpiresAt); err != nil {
		return Link{}, err
	}
	s.seen = nil
	return l, nil
}

// moment is now, by every clock setup keeps.
func (s *Setup) moment() store.Moment {
	return store.Moment{Wall: s.clock.Now(), Boot: s.clock.Boot(), Since: s.clock.Elapsed()}
}

// age is how long ago then was, if it is known: in this boot. After another boot, it isn't.
func age(now, then store.Moment) (time.Duration, bool) {
	if now.Boot == "" || then.Boot != now.Boot {
		return 0, false
	}
	return now.Since - then.Since, true
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
	// Whether it was claimed or revoked, ClaimLink says, at once with using it up.
	now := s.moment()
	if old, ok := age(now, row.Made); !ok || !within(old, LinkLife) {
		return "", ErrLinkRefused
	}
	session, sessionHash := token()
	err = s.db.ClaimLink(ctx, hash, sessionHash, client, now)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrLinkRefused
	}
	if err != nil {
		return "", err
	}
	s.seen = &lastUse{hash: sessionHash, seen: now.Since, kept: now.Since}
	return session, nil
}

// Session checks a session's token, from client, and keeps the session open. The token travels in clear on the LAN,
// so it is good only from the device that claimed the link, and for a while. The database says whether the session
// exists, wasn't ended, and when it was opened; its last use is this process's or, after a restart, the database's.
// That last use is written at most every SeenEvery, outside the lock, and a write that fails doesn't refuse the
// session: reading setup must work on a full disk.
func (s *Setup) Session(ctx context.Context, tok, client string) error {
	hash := digest(tok)
	now, write, err := s.check(ctx, hash, client)
	if err != nil {
		return err
	}
	if write {
		_ = s.db.SeeSession(ctx, hash, now) // tried again a minute later
	}
	return nil
}

// check checks a session under the lock, and says whether it is time to write its last use.
func (s *Setup) check(ctx context.Context, hash []byte, client string) (store.Moment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.db.Session(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return store.Moment{}, false, ErrNoSession
	}
	if err != nil {
		return store.Moment{}, false, err
	}
	now := s.moment()
	use := s.seen
	if use == nil || !bytes.Equal(use.hash, hash) {
		use = &lastUse{hash: hash, seen: row.Seen.Since, kept: row.Seen.Since} // opened before a restart
	}
	opened, ok := age(now, row.Opened)
	if !ok || row.Client != client || !within(opened, SessionMax) || !within(now.Since-use.seen, SessionIdle) {
		return store.Moment{}, false, ErrNoSession
	}
	use.seen = now.Since
	s.seen = use
	if now.Since-use.kept < SeenEvery {
		return now, false, nil
	}
	use.kept = now.Since
	return now, true, nil
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
