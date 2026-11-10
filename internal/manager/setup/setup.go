// Package setup is first-time setup: its steps in two halves, the one-time link that opens it, and the session that
// link becomes. The steps and their state live in the manager's database, so setup resumes where it stopped.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// Invalid is an answer that didn't pass its checks, naming the fields to fix.
type Invalid struct{ Fields []string }

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
)

// Setup is first-time setup on this primary.
type Setup struct {
	db       *store.Store
	now      func() time.Time
	handlers map[string]Handler
}

// New is first-time setup kept in db. Handlers answer the steps that are built; a step without one is not built yet.
func New(db *store.Store, now func() time.Time, handlers map[string]Handler) *Setup {
	return &Setup{db: db, now: now, handlers: handlers}
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
	for _, earlier := range st.Steps[:i] {
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
	if err := s.db.CompleteStep(ctx, id, keep, s.now()); err != nil {
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
	now := s.now()
	l := Link{Key: key, ExpiresAt: now.Add(LinkLife)}
	return l, s.db.NewLink(ctx, hash, now, l.ExpiresAt)
}

// Claim uses up a link and returns the token of the session it opens.
func (s *Setup) Claim(ctx context.Context, key string) (string, error) {
	session, sessionHash := token()
	err := s.db.ClaimLink(ctx, digest(key), sessionHash, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrLinkRefused
	}
	return session, err
}

// Session checks a session's token, and keeps the session open.
func (s *Setup) Session(ctx context.Context, tok string) error {
	err := s.db.TouchSession(ctx, digest(tok), s.now(), SessionIdle)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNoSession
	}
	return err
}

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
