package manager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
)

// Admin signs people in with the household account: it is an OIDC client of the sign-in, whose flow for it takes a
// passkey and nothing else (ADR 0004, amended). Only admins may sign in. The first admin's first sign-in, which can
// only come over the mesh (the edge serves Admin there alone), closes the setup link's LAN listener for good (#17).
//
// Admin's cookies are __Host- prefixed: Secure, Path=/, no Domain, so no other name of the household can set or
// shadow them (#83).
const (
	sessionCookie = "__Host-homerslab-session"
	signInCookie  = "__Host-homerslab-sign-in"
	// SessionLife is how long an Admin session lasts.
	SessionLife = 12 * time.Hour
	// attemptLife is how long a sign-in may take, from Admin to the sign-in and back.
	attemptLife = 10 * time.Minute
)

// accountRoutes are the household account's routes in Admin: the sign-in, and first-time setup's last step.
func (m *Manager) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/sign-in", m.signInStart)
	mux.HandleFunc("GET /auth/callback", m.signInDone)
	mux.HandleFunc("POST /auth/sign-out", m.signOut)
	mux.Handle("GET /account/{$}", m.signedIn(http.HandlerFunc(m.accountPage)))
	mux.Handle("GET /api/v1/account/setup", m.signedIn(http.HandlerFunc(m.accountSetup)))
	mux.Handle("PUT /api/v1/account/setup/steps/{id}", m.signedIn(http.HandlerFunc(m.accountStep)))
	mux.HandleFunc("GET /assets/{file}", m.asset)
}

// attempts are sign-ins under way, by the random ID in the browser's sign-in cookie. They live in memory only: a
// manager that restarts asks the person to start again.
type attempts struct {
	mu sync.Mutex
	m  map[string]attempt
}

type attempt struct {
	oidc.Attempt
	expires time.Time
}

// maxAttempts is how many sign-ins may be under way at once. Beyond it, a new one is refused rather than ending a live
// one: a peer flooding /auth/sign-in never evicts an admin's sign-in.
const maxAttempts = 256

func (as *attempts) put(id string, a attempt, now time.Time) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.m == nil {
		as.m = map[string]attempt{}
	}
	for k, v := range as.m {
		if now.After(v.expires) {
			delete(as.m, k)
		}
	}
	if len(as.m) >= maxAttempts {
		return false
	}
	as.m[id] = a
	return true
}

func (as *attempts) take(id string, now time.Time) (attempt, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	a, ok := as.m[id]
	delete(as.m, id)
	return a, ok && now.Before(a.expires)
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func setCookie(w http.ResponseWriter, name, value string, life time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if life > 0 {
		c.MaxAge = int(life / time.Second)
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// adminOIDC is Admin's client of the sign-in, reaching the provider's server on this machine.
func (m *Manager) adminOIDC(ctx context.Context, ans map[string]string) (oidc.Client, error) {
	if ans[ansClientID] == "" || ans[ansIssuer] == "" {
		return oidc.Client{}, errors.New("Admin's client isn't made yet")
	}
	secret, err := m.agent.unseal(ctx, credAdminClient)
	if err != nil {
		return oidc.Client{}, err
	}
	return oidc.Client{Issuer: ans[ansIssuer], ID: ans[ansClientID], Secret: secret, Redirect: AdminURL(ans) + "/auth/callback",
		HTTP: &http.Client{Timeout: 15 * time.Second, Transport: oidc.Through(household(ans).Origin(), m.upstream())}}, nil
}

// signInStart sends the browser to the sign-in, with a fresh attempt bound to it by a cookie.
func (m *Manager) signInStart(w http.ResponseWriter, r *http.Request) {
	ans, err := m.db.Answers(r.Context())
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	c, err := m.adminOIDC(r.Context(), ans)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "no-sign-in-yet")
		return
	}
	e, err := c.Discover(r.Context())
	if err != nil {
		m.log.Warn("the sign-in's discovery document", "err", err)
		fail(w, http.StatusBadGateway, "sign-in-unreachable")
		return
	}
	// Always a fresh sign-in: the passkey every time, never a session the person holds at the sign-in already, which
	// the provider's own ways in could have made (#84).
	a := oidc.FreshAttempt(m.cfg.Now())
	id := random()
	if !m.attempts.put(id, attempt{Attempt: a, expires: m.cfg.Now().Add(attemptLife)}, m.cfg.Now()) {
		fail(w, http.StatusTooManyRequests, "too-many-sign-ins")
		return
	}
	setCookie(w, signInCookie, id, attemptLife)
	http.Redirect(w, r, c.AuthorizeURL(e, a), http.StatusSeeOther)
}

// signInDone takes the browser back from the sign-in: the attempt must be this browser's, the code good, the ID token
// verified, and the person an admin. Then Admin opens a session. The first admin's first sign-in finishes setup's
// passkey step and closes the LAN listener for good.
func (m *Manager) signInDone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ck, err := r.Cookie(signInCookie)
	setCookie(w, signInCookie, "", 0)
	if err != nil {
		fail(w, http.StatusForbidden, "no-sign-in-attempt")
		return
	}
	a, ok := m.attempts.take(ck.Value, m.cfg.Now())
	if !ok {
		fail(w, http.StatusForbidden, "no-sign-in-attempt")
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
		fail(w, http.StatusBadGateway, "sign-in-unreachable")
		return
	}
	q := r.URL.Query()
	cl, err := c.Finish(ctx, e, a.Attempt, q.Get("state"), q.Get("code"), m.cfg.Now())
	if err != nil {
		m.log.Warn("refused a sign-in to Admin", "err", err)
		fail(w, http.StatusForbidden, "sign-in-refused")
		return
	}
	if !slices.Contains(cl.Groups, idp.RoleGroup(idp.Admin)) || cl.Username == "" {
		m.log.Warn("refused a sign-in to Admin: not an admin", "username", cl.Username)
		fail(w, http.StatusForbidden, "not-an-admin")
		return
	}
	if ans[ansSignedInAt] == "" && cl.Username == ans[ansAdminUser] {
		if err := m.firstSignIn(ctx, ans); err != nil {
			m.log.Error("the first admin's first sign-in", "err", err)
			fail(w, http.StatusBadGateway, "first-sign-in-failed")
			return
		}
	}
	tok := random()
	now := m.cfg.Now()
	if err := m.db.NewAdminSession(ctx, hash(tok), cl.Username, now, now.Add(SessionLife)); err != nil {
		m.internal(w, "opening a session", err)
		return
	}
	setCookie(w, sessionCookie, tok, SessionLife)
	m.log.Info("signed in to Admin", "username", cl.Username)
	to := "/"
	if st, err := m.setup.State(ctx); err == nil && !done(st, "done") {
		to = "/account/" // first-time setup's last step
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// firstSignIn is the first admin's first sign-in over the mesh: their device is on the mesh and their passkey works,
// so the join and passkey steps are done, and the setup link's LAN listener closes for good (the firewall table drops
// its exception, and the listener answers nothing from then on). The device key has done its work, so it ends.
func (m *Manager) firstSignIn(ctx context.Context, ans map[string]string) error {
	if !m.lanClosed.Load() {
		if err := m.CloseLANListener(ctx); err != nil {
			return err
		}
	}
	if id := ans[ansDeviceKey]; id != "" {
		t, err := m.currentToken(ctx, ans)
		var nb mesh.Mesh
		if err == nil {
			nb, err = m.netbird.Open(ans[ansURL], t)
		}
		if err == nil {
			err = nb.RevokeKey(ctx, id)
		}
		if err != nil {
			return fmt.Errorf("ending the admin's device key: %w", err)
		}
	}
	now := m.cfg.Now()
	for _, id := range []string{"join", "passkey"} {
		if err := m.db.CompleteStep(ctx, id, nil, now); err != nil {
			return err
		}
	}
	return m.db.SaveAnswers(ctx, map[string]string{ansSignedInAt: now.UTC().Format(time.RFC3339)})
}

// signOut ends the browser's Admin session.
func (m *Manager) signOut(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		_ = m.db.EndAdminSession(r.Context(), hash(ck.Value), m.cfg.Now())
	}
	setCookie(w, sessionCookie, "", 0)
	w.WriteHeader(http.StatusNoContent)
}

type ctxUser struct{}

// signedIn wants a live Admin session; without one, a page goes to the sign-in and an API call is refused.
func (m *Manager) signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := "", store.ErrNotFound
		if ck, cerr := r.Cookie(sessionCookie); cerr == nil && ck.Value != "" {
			user, err = m.db.AdminSession(r.Context(), hash(ck.Value), m.cfg.Now())
		}
		if errors.Is(err, store.ErrNotFound) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				fail(w, http.StatusUnauthorized, "signed-out")
				return
			}
			http.Redirect(w, r, "/auth/sign-in", http.StatusSeeOther)
			return
		}
		if err != nil {
			m.internal(w, "checking a session", err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, user)))
	})
}

// accountSetup is setup's state for a signed-in admin, with what the last step needs: how many passkeys they have,
// and whether another admin has one.
func (m *Manager) accountSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := m.setup.State(ctx)
	if err != nil {
		m.internal(w, "reading setup", err)
		return
	}
	ans := st.Answers
	out := map[string]any{"current": st.Current, "half": st.Half, "steps": st.Steps, "admin": AdminURL(ans),
		"household": ans["household"], "command": "sudo homerslab setup passkey"}
	if p, err := m.provider(ctx, ans); err == nil && !done(st, "done") {
		if keys, err := p.Passkeys(ctx, ans[ansAdminUser]); err == nil {
			out["passkeys"] = len(keys)
			if ok, err := twoWaysIn(ctx, p, ans[ansAdminUser], len(keys)); err == nil {
				out["twoWaysIn"] = ok
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// accountStep takes a mesh step's answer from a signed-in admin: the last one, which finishes first-time setup.
func (m *Manager) accountStep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "done" {
		fail(w, http.StatusNotFound, "unknown-step")
		return
	}
	st, err := m.setup.Answer(r.Context(), id, setup.Mesh, []byte(`{}`))
	switch {
	case err == nil:
		m.log.Info("first-time setup is done", "by", r.Context().Value(ctxUser{}))
		writeJSON(w, http.StatusOK, st)
	case errors.Is(err, ErrOneWayIn):
		fail(w, http.StatusConflict, ErrOneWayIn.Error())
	case errors.Is(err, setup.ErrEarlierStepOpen):
		fail(w, http.StatusConflict, err.Error())
	default:
		m.internal(w, "finishing setup", err)
	}
}

// accountPage is the page of setup's last step in Admin, from the built web app.
func (m *Manager) accountPage(w http.ResponseWriter, _ *http.Request) {
	b, err := fs.ReadFile(m.web, "account.html")
	if err != nil {
		fail(w, http.StatusNotImplemented, "not-built")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
