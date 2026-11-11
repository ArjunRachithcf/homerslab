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
	"strings"
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

// Handler checks a step's answer and returns what to keep of it; earlier are the answers so far. Answers kept here are
// never secret. A step that needs a secret returns *NeedsSecret.
type Handler func(ctx context.Context, answer json.RawMessage, earlier map[string]string) (map[string]string, error)

// SecretHandler takes a secret a waiting step asked for, given in this machine's terminal over the control socket,
// and finishes the step: it returns what to keep, none of it secret.
type SecretHandler func(ctx context.Context, value string, earlier map[string]string) (map[string]string, error)

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
	secrets  map[string]SecretHandler

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
// Secrets take the secrets that waiting steps ask for, by the secret's name.
func New(db *store.Store, clock Clock, handlers map[string]Handler, secrets map[string]SecretHandler) *Setup {
	return &Setup{db: db, clock: clock, handlers: handlers, secrets: secrets}
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
		err = s.db.CompleteStep(ctx, id, keep, s.clock.Now())
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
