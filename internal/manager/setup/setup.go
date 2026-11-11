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
	"strings"
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

// Handler checks a step's answer and returns what to keep of it; earlier are the answers so far. Answers kept here are
// never secret. A step that needs a secret returns *NeedsSecret.
type Handler func(ctx context.Context, answer json.RawMessage, earlier map[string]string) (map[string]string, error)

// SecretHandler takes a secret a waiting step asked for, given in this machine's terminal over the control socket,
// and finishes the step: it returns what to keep, none of it secret.
type SecretHandler func(ctx context.Context, value string, earlier map[string]string) (map[string]string, error)

// Invalid is an answer that didn't pass its checks, naming the fields to fix.
type Invalid struct{ Fields []string }

func (e *Invalid) Error() string { return fmt.Sprintf("invalid answer: %v", e.Fields) }

// NeedsSecret is a handler's answer when its step can't be finished without a secret: the step keeps Keep and waits
// for the secret named Secret, which arrives through `homerslab setup secret <Secret>` (#64).
type NeedsSecret struct {
	Secret string
	Keep   map[string]string
}

func (e *NeedsSecret) Error() string { return "waiting for the secret " + e.Secret }

// NeedsConfirm is a handler's answer when its step found something only the admin may say yes to (Confirm names
// it); the step keeps Keep and waits for the answer again with the yes.
type NeedsConfirm struct {
	Confirm string
	Keep    map[string]string
}

func (e *NeedsConfirm) Error() string { return "waiting for the admin's yes to " + e.Confirm }

// SecretOverHTTP refuses an answer over the LAN page that carries a secret: no secret travels over plain HTTP (#64).
// Command is what to run in the machine's terminal instead.
type SecretOverHTTP struct{ Command string }

func (e *SecretOverHTTP) Error() string { return "secret-over-http" }

// Secrets are the names of the secrets setup asks for, by the step that needs them. Each is given with
// `sudo homerslab setup secret <name>`.
var Secrets = map[string]string{"network": "mesh", "domain": "dns", "mail": "mail"}

// Command is the terminal command that gives a secret.
func Command(secret string) string { return "sudo homerslab setup secret " + secret }

// secretParts are what the name of a field holding a secret contains, whatever its case. An answer from the LAN page
// with such a field is refused, whatever its value.
var secretParts = []string{"token", "secret", "password", "passwd", "key", "credential", "pat"}

// secretName says whether a field's name says it holds a secret.
func secretName(k string) bool {
	k = strings.ToLower(k)
	if k == "pat" {
		return true
	}
	for _, p := range secretParts {
		if p != "pat" && strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// The ways a request can be refused.
var (
	ErrUnknownStep     = errors.New("unknown-step")
	ErrOverTheMesh     = errors.New("over-the-mesh")     // a mesh step, asked over the LAN link
	ErrEarlierStepOpen = errors.New("earlier-step-open") // steps are taken in order
	ErrNotBuilt        = errors.New("not-built")         // a step a later version of HomersLab brings
	ErrLinkRefused     = errors.New("link-refused")      // unknown, used, ended or expired
	ErrNoSession       = errors.New("no-session")
	ErrLANHalfDone     = errors.New("lan-half-done") // setup continues over the mesh
	ErrNotWaiting      = errors.New("not-waiting")   // a secret no step is waiting for
)

const (
	// LinkLife is how long a new link can be claimed.
	LinkLife = 30 * time.Minute
	// SessionIdle is how long a claimed link's session stays open without a visit.
	SessionIdle = 30 * time.Minute
	// SessionMax is how long a session lasts at most; then a new link continues where setup stopped.
	SessionMax = 2 * time.Hour
)

// Setup is first-time setup on this primary.
type Setup struct {
	db       *store.Store
	now      func() time.Time
	handlers map[string]Handler
	secrets  map[string]SecretHandler
}

// New is first-time setup kept in db. Handlers answer the steps that are built; a step without one is not built yet.
// Secrets take the secrets that waiting steps ask for, by the secret's name.
func New(db *store.Store, now func() time.Time, handlers map[string]Handler, secrets map[string]SecretHandler) *Setup {
	return &Setup{db: db, now: now, handlers: handlers, secrets: secrets}
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
	State string `json:"state"` // todo, waiting (for Secret, given in the terminal) or done
	// Secret is the secret a waiting step needs, and Command what gives it; or Confirm, what it asks a yes to.
	Secret  string `json:"secret,omitempty"`
	Command string `json:"command,omitempty"`
	Confirm string `json:"confirm,omitempty"`
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
		ss := StepState{ID: r.ID, Half: Half(r.Half), State: "todo"}
		if r.Done {
			ss.State = "done"
		} else {
			switch r.WaitKind {
			case "secret":
				ss.State, ss.Secret, ss.Command = "waiting", r.WaitName, Command(r.WaitName)
			case "confirm":
				ss.State, ss.Confirm = "waiting", r.WaitName
			}
			if st.Current == "" {
				st.Current, st.Half = r.ID, Half(r.Half)
			}
		}
		st.Steps = append(st.Steps, ss)
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
	if over == LAN && carriesSecret(answer) {
		secret := Secrets[id]
		if secret == "" {
			secret = "<name>"
		}
		return State{}, &SecretOverHTTP{Command: Command(secret)}
	}
	h := s.handlers[id]
	if h == nil {
		return State{}, ErrNotBuilt
	}
	keep, err := h(ctx, answer, st.Answers)
	return s.settle(ctx, id, keep, err)
}

// settle keeps what a handler returned: the step done, waiting, or as it was with the handler's error.
func (s *Setup) settle(ctx context.Context, id string, keep map[string]string, err error) (State, error) {
	var secret *NeedsSecret
	var confirm *NeedsConfirm
	switch {
	case errors.As(err, &secret):
		err = s.db.Wait(ctx, id, "secret", secret.Secret, secret.Keep)
	case errors.As(err, &confirm):
		err = s.db.Wait(ctx, id, "confirm", confirm.Confirm, confirm.Keep)
	case err == nil:
		err = s.db.CompleteStep(ctx, id, keep, s.now())
	}
	if err != nil {
		return State{}, err
	}
	return s.State(ctx)
}

// carriesSecret says whether an answer has a field named like a secret, at any depth.
func carriesSecret(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, inner := range x {
				if secretName(k) || walk(inner) {
					return true
				}
			}
		case []any:
			for _, inner := range x {
				if walk(inner) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

// Secret takes a secret, by name, for the step waiting for it, and finishes that step. It comes from this machine's
// terminal over the control socket, never from the LAN page.
func (s *Setup) Secret(ctx context.Context, name, value string) (State, error) {
	st, err := s.State(ctx)
	if err != nil {
		return State{}, err
	}
	var step string
	for _, ss := range st.Steps {
		if ss.State == "waiting" && ss.Secret == name {
			step = ss.ID
		}
	}
	h := s.secrets[name]
	if step == "" || h == nil {
		return State{}, ErrNotWaiting
	}
	keep, err := h(ctx, value, st.Answers)
	return s.settle(ctx, step, keep, err)
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

// Claim uses up a link and returns the token of the session it opens for client, the claiming device's address.
func (s *Setup) Claim(ctx context.Context, key, client string) (string, error) {
	session, sessionHash := token()
	err := s.db.ClaimLink(ctx, digest(key), sessionHash, client, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrLinkRefused
	}
	return session, err
}

// Session checks a session's token, from client, and keeps the session open. The token travels in clear on the LAN,
// so it is good only from the device that claimed the link, and for a while.
func (s *Setup) Session(ctx context.Context, tok, client string) error {
	err := s.db.TouchSession(ctx, digest(tok), client, s.now(), SessionIdle, SessionMax)
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
