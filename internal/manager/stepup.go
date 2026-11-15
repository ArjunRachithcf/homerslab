package manager

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
)

// Step-up (#25; spec: Security, Adapter contracts; glossary: Step-up, Risky change): a risky change asks for the
// admin's passkey again, every time, right before it is made. It is the one way Admin lets a risky change through, so a
// stolen session alone, or a token, makes none.
//
//  1. Admin's page asks to step up for one action: a risky plan, by its id ("plan:<id>"), or a risky setting with the
//     change itself (the backup target and its credentials), which the step-up keeps only as a digest. The manager
//     answers with the sign-in's authorize URL of Admin's own client: max_age is used, so the passkey-only sign-in runs
//     again, and the nonce is derived from the attempt's own secret, the action and the Admin session.
//  2. The sign-in sends the browser back to Admin's callback. The step-up passes only if the ID token is good, its
//     nonce is this attempt's, the person signed in after the step-up began, they are the session's person and an
//     admin, and they hold passkeys and nothing else, as the provider lists it now. Anything else there refuses it and
//     raises a security event (#84).
//  3. That leaves a grant, in memory only: for this Admin session and this one action (for a setting, that exact
//     change), for StepUpLife from the passkey's assertion, used once. Making the change takes it, once everything
//     else about the change has been checked, and checks again that the person is still an admin. Another session,
//     another device, another action or another change never finds it.
//
// The step-up's sign-in must be made after the step-up began, by the issuer's auth_time: the sign-in runs on the
// primary, on the manager's own clock.
//
// No token can step up: a grant is made only by a browser bringing a code back to Admin's callback, for the session
// cookie it holds, and Admin takes no token of any kind. The machine's terminal (the control socket, root on the
// primary) needs no step-up: root there can already do anything.

const (
	stepUpCookie = "__Host-homerslab-step-up"
	// StepUpLife is how long a step-up approves its action for, from the passkey's assertion: the time to apply it.
	StepUpLife = 5 * time.Minute
	// maxStepUps is how many step-ups and grants may be kept at once, so a flood never evicts an admin's.
	maxStepUps = 256
)

// The actions a step-up approves that aren't plans: risky settings, by name.
const (
	actionBackupTarget = "backups/target"
)

// The ways a step-up ends, as the page is sent back with them (?step-up=).
const (
	stepUpDone     = "done"
	stepUpTooSoon  = "too-soon"      // the sign-in passed on a session it had from just before: try again in a moment
	stepUpRefused  = "refused"       // a bad code or token, another person, or not an admin
	stepUpNotKeyed = "passkeys-only" // the admin holds something besides passkeys (#84): homerslab admin recover
)

type stepUpAttempt struct {
	oidc.Attempt
	browser []byte // the hash of the browser's step-up cookie
	session []byte // the Admin session's token hash
	user    string
	action  string
	back    string // the page that asked, which the browser goes back to
	expires time.Time
}

type grant struct {
	user    string
	expires time.Time
}

// stepUps are the step-ups under way, by their state, each bound to the browser's step-up cookie (one per browser, so
// two tabs may step up at once), and the grants they left, by session and action. Both live in memory only: a
// manager that restarts asks for the passkey again.
type stepUps struct {
	mu       sync.Mutex
	attempts map[string]stepUpAttempt
	grants   map[string]grant
}

func grantKey(session []byte, action string) string {
	return hex.EncodeToString(session) + "\x00" + action
}

func (s *stepUps) prune(now time.Time) {
	for k, a := range s.attempts {
		if !now.Before(a.expires) {
			delete(s.attempts, k)
		}
	}
	for k, g := range s.grants {
		if !now.Before(g.expires) {
			delete(s.grants, k)
		}
	}
}

func (s *stepUps) start(a stepUpAttempt, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts, s.grants = map[string]stepUpAttempt{}, map[string]grant{}
	}
	s.prune(now)
	if len(s.attempts) >= maxStepUps {
		return false
	}
	s.attempts[a.State] = a
	return true
}

// finish takes the step-up the sign-in's state names, if it is this browser's: anything else (a sign-in's own
// callback) leaves it.
func (s *stepUps) finish(browser []byte, state string, now time.Time) (stepUpAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[state]
	if !ok || state == "" || subtle.ConstantTimeCompare(browser, a.browser) != 1 {
		return stepUpAttempt{}, false
	}
	delete(s.attempts, state)
	return a, now.Before(a.expires)
}

func (s *stepUps) grant(session []byte, action string, g grant, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	if len(s.grants) >= maxStepUps {
		return false
	}
	s.grants[grantKey(session, action)] = g
	return true
}

// take uses a grant up: it is there once, for its session and action, until it expires.
func (s *stepUps) take(session []byte, action string, now time.Time) (grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := grantKey(session, action)
	g, ok := s.grants[k]
	delete(s.grants, k)
	return g, ok && now.Before(g.expires)
}

// stepUpNonce is a step-up's nonce: the attempt's own secret, the action and the session's hash, hashed, so the ID
// token the sign-in hands back carries this one step-up's binding, and nothing another attempt could have asked for.
// Finish compares it.
func stepUpNonce(secret string, session []byte, action string) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte("homerslab step-up"), []byte(secret), []byte(action), session} {
		h.Write([]byte{byte(len(part) >> 8), byte(len(part))})
		h.Write(part)
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// signedInAs is the person and the session hash signedIn found for the request, or empty.
func signedInAs(r *http.Request) (string, []byte) {
	user, _ := r.Context().Value(ctxUser{}).(string)
	sess, _ := r.Context().Value(ctxSession{}).([]byte)
	if user == "" || len(sess) == 0 {
		return "", nil
	}
	return user, sess
}

// settingAction is the action a risky setting's step-up approves: the setting and a digest of the change itself, as
// JSON with its keys in order, so the step-up approves that change and no other.
func settingAction(setting string, change json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(change, &v); err != nil || v == nil {
		return "", errors.New("not a change")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return setting + ":" + hex.EncodeToString(sum[:]), nil
}

// stepUpAction says whether an action is a risky change a step-up approves, and which page asked for it.
func (m *Manager) stepUpAction(ctx context.Context, action string) (back string, ok bool, err error) {
	if strings.HasPrefix(action, actionBackupTarget+":") {
		return "/", true, nil
	}
	id, isPlan := strings.CutPrefix(action, "plan:")
	if !isPlan || id == "" {
		return "", false, nil
	}
	b, err := m.db.Plan(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var p plan.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return "", false, err
	}
	risky := p.Kind == "restore" || (p.Kind == "install" && p.Risky)
	return "/plans/" + url.PathEscape(id), risky, nil
}

// postStepUp starts a step-up for one action of the signed-in admin: the answer is where the browser goes for the
// passkey.
func (m *Manager) postStepUp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Action string `json:"action"`
		// Change is a risky setting's change, as it will be sent: kept only as its digest, never itself.
		Change json.RawMessage `json:"change,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	user, sess := signedInAs(r)
	if user == "" {
		fail(w, http.StatusUnauthorized, "signed-out")
		return
	}
	switch {
	case body.Action == actionBackupTarget:
		a, err := settingAction(body.Action, body.Change)
		if err != nil {
			fail(w, http.StatusBadRequest, "no-change")
			return
		}
		body.Action = a
	case len(body.Change) > 0 || strings.Contains(body.Action, actionBackupTarget):
		fail(w, http.StatusUnprocessableEntity, "not-a-risky-change")
		return
	}
	back, ok, err := m.stepUpAction(ctx, body.Action)
	if err != nil {
		m.internal(w, "reading what a step-up is for", err)
		return
	}
	if !ok {
		fail(w, http.StatusUnprocessableEntity, "not-a-risky-change")
		return
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	c, err := m.adminOIDC(ctx, ans)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "no-sign-in-yet")
		return
	}
	e, err := c.Discover(ctx)
	if err != nil {
		m.log.Warn("the sign-in's discovery document, for a step-up", "err", err)
		fail(w, http.StatusBadGateway, "sign-in-unreachable")
		return
	}
	now := m.cfg.Now()
	// One cookie for the browser's step-ups, kept while it has one: a second tab's doesn't end the first's.
	id := random()
	if ck, err := r.Cookie(stepUpCookie); err == nil && len(ck.Value) == len(id) {
		id = ck.Value
	}
	a := stepUpAttempt{Attempt: oidc.StepUpAttempt(now, stepUpNonce(random(), sess, body.Action)), browser: hash(id), session: sess,
		user: user, action: body.Action, back: back, expires: now.Add(attemptLife)}
	if !m.stepUps.start(a, now) {
		fail(w, http.StatusTooManyRequests, "too-many-step-ups")
		return
	}
	setCookie(w, stepUpCookie, id, attemptLife)
	m.log.Info("a step-up began", "username", a.user, "action", a.action)
	writeJSON(w, http.StatusOK, map[string]string{"to": c.AuthorizeURL(e, a.Attempt)})
}

// stepUpReturn takes the browser back from the sign-in, for a step-up under way in it; false when the callback isn't
// one (a sign-in's).
func (m *Manager) stepUpReturn(w http.ResponseWriter, r *http.Request) bool {
	ck, err := r.Cookie(stepUpCookie)
	if err != nil {
		return false
	}
	q := r.URL.Query()
	a, ok := m.stepUps.finish(hash(ck.Value), q.Get("state"), m.cfg.Now())
	if !ok && a.back == "" {
		return false // no step-up of this browser's: a sign-in's callback
	}
	outcome := m.stepUpCheck(r, a, ok)
	http.Redirect(w, r, a.back+"?step-up="+outcome, http.StatusSeeOther)
	return true
}

// stepUpCheck checks a step-up's return, and leaves the grant when it passes.
func (m *Manager) stepUpCheck(r *http.Request, a stepUpAttempt, live bool) string {
	ctx := r.Context()
	refuse := func(why string, args ...any) string {
		m.log.Warn("refused a step-up: "+why, append([]any{"username", a.user, "action", a.action}, args...)...)
		return stepUpRefused
	}
	if !live {
		return refuse("it took too long")
	}
	// The browser must still be in the Admin session that asked: a code another browser brings opens nothing.
	var sess []byte
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
		sess = hash(ck.Value)
	}
	if sess == nil || subtle.ConstantTimeCompare(sess, a.session) != 1 {
		return refuse("not the session that asked")
	}
	if user, err := m.db.AdminSession(ctx, sess, m.cfg.Now()); err != nil || user != a.user {
		return refuse("the session ended", "err", err)
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return refuse("reading the answers", "err", err)
	}
	c, err := m.adminOIDC(ctx, ans)
	if err != nil {
		return refuse("no sign-in", "err", err)
	}
	e, err := c.Discover(ctx)
	if err != nil {
		return refuse("the sign-in is unreachable", "err", err)
	}
	q := r.URL.Query()
	cl, err := c.Finish(ctx, e, a.Attempt, q.Get("state"), q.Get("code"), m.cfg.Now())
	if errors.Is(err, oidc.ErrNotFresh) {
		m.log.Warn("a step-up passed on a sign-in from before it began: asked to try again", "username", a.user, "action", a.action)
		return stepUpTooSoon
	}
	if err != nil {
		return refuse("the sign-in's answer", "err", err)
	}
	if cl.Username != a.user || !slices.Contains(cl.Groups, idp.RoleGroup(idp.Admin)) {
		return refuse("not the session's admin", "signedIn", cl.Username)
	}
	p, err := m.provider(ctx, ans)
	if err != nil {
		return refuse("the household account", "err", err)
	}
	creds, err := p.Credentials(ctx, a.user)
	if err != nil {
		return refuse("reading the admin's credentials", "err", err)
	}
	if !creds.PasskeysOnly() {
		// A security event (ADR 0018): something besides passkeys is on an admin's account, which HomersLab never puts
		// there. The way out is another admin, or homerslab admin recover on the primary (#87).
		m.log.Error("refused a step-up: the admin holds more than passkeys", "username", a.user, "others", creds.Others, "passkeys", creds.Passkeys)
		m.warnOnce(ctx, warnStepUp+a.user, "security: "+a.user+" holds more than passkeys ("+strings.Join(creds.Others, ", ")+
			"), so their passkey approves no risky change. Another admin, or sudo homerslab admin recover on the primary, puts it right.")
		return stepUpNotKeyed
	}
	if !m.stepUps.grant(a.session, a.action, grant{user: a.user, expires: cl.AuthTime.Add(StepUpLife)}, m.cfg.Now()) {
		return refuse("too many step-ups under way")
	}
	m.log.Info("a step-up passed", "username", a.user, "action", a.action)
	return stepUpDone
}

// warnStepUp is the security event's source, per admin.
const warnStepUp = "security:step-up:"

// passStepUp is the step-up a risky change from Admin needs: this session's grant for this action, used up, and the
// person still an admin. It comes last before the change is made, so a change refused for another reason leaves the
// grant for the retry.
func (m *Manager) passStepUp(r *http.Request, action string) error {
	ctx := r.Context()
	user, sess := signedInAs(r)
	if user == "" {
		return errStepUp
	}
	g, ok := m.stepUps.take(sess, action, m.cfg.Now())
	if !ok || g.user != user {
		return errStepUp
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return err
	}
	p, err := m.provider(ctx, ans)
	if err != nil {
		return err
	}
	admins, err := p.People(ctx, idp.Admin)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(admins, func(x idp.Person) bool { return x.Username == user }) {
		m.log.Warn("refused a risky change: the person is no longer an admin", "username", user, "action", action)
		return errStepUp
	}
	m.log.Info("a risky change, approved by its step-up", "username", user, "action", action)
	return nil
}

// stepUpRefusal answers a risky change that passStepUp refused.
func (m *Manager) stepUpRefusal(w http.ResponseWriter, err error) {
	if !errors.Is(err, errStepUp) {
		m.log.Warn("checking a step-up", "err", err)
	}
	fail(w, http.StatusForbidden, errStepUp.Error())
}
