package manager_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const (
	ownToken     = "ak-OWN-TOKEN-PLACEHOLDER-000000000000000000000"
	clientSecret = "ak-CLIENT-SECRET-PLACEHOLDER-0000000000000000"
	adminOrigin  = "https://admin.example.test:9443"
	signInOrigin = "https://auth.example.test:9443"
)

// fakeSignIn stands in for Authentik: the provider's API as the adapter gives it, and an OIDC issuer at its upstream
// that signs ID tokens with a key of its own.
type fakeSignIn struct {
	mu        sync.Mutex
	t         *testing.T
	agentSock string
	notReady  int // how many readiness checks fail first
	bootstrap []string
	// servedAtBootstrap is the edge's sign-in name when the bootstrap ran: it must be empty.
	servedAtBootstrap []string
	prepared          int
	people            map[string]idp.Person
	passkeys          map[string]int
	links             []string
	issuer            *httptest.Server
	key               *rsa.PrivateKey
	codes             map[string]map[string]any
	now               func() time.Time // the manager's clock
	// beforeKeep and afterKeep run around the manager's keeping of its new token; unkept counts the ones it couldn't.
	beforeKeep, afterKeep func()
	unkept                int
}

func newFakeSignIn(t *testing.T) *fakeSignIn {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeSignIn{t: t, people: map[string]idp.Person{}, passkeys: map[string]int{}, key: k, codes: map[string]map[string]any{}}
	f.issuer = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.issuer.Close)
	return f
}

const issuer = signInOrigin + "/application/o/homerslab-admin/"

func (f *fakeSignIn) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != "auth.example.test:9443" || r.Header.Get("X-Forwarded-Proto") != "https" {
		http.Error(w, "not the sign-in's origin", http.StatusMisdirectedRequest)
		return
	}
	switch r.URL.Path {
	case "/application/o/homerslab-admin/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "authorization_endpoint": signInOrigin + "/application/o/authorize/",
			"token_endpoint": signInOrigin + "/application/o/token/", "jwks_uri": signInOrigin + "/application/o/homerslab-admin/jwks/"})
	case "/application/o/homerslab-admin/jwks/":
		e := big.NewInt(int64(f.key.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	case "/application/o/token/":
		id, secret, _ := r.BasicAuth()
		_ = r.ParseForm()
		f.mu.Lock()
		claims, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if id != "admin-client" || secret != clientSecret || !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != claims["_challenge"] {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(claims, "_challenge")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(claims)})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSignIn) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// code is what the sign-in hands the browser after a person signed in for an authorize URL: just now when the URL
// asks for a recent sign-in (max_age), as Authentik does, and otherwise on the session they signed in to an hour
// before.
func (f *fakeSignIn) code(t *testing.T, authorize, username string, groups ...string) (state, code string) {
	t.Helper()
	at := f.now().Add(-time.Hour)
	if u, err := url.Parse(authorize); err == nil && u.Query().Get("max_age") != "" {
		at = f.now()
	}
	return f.codeAt(t, authorize, at, username, groups...)
}

// codeAt is code, for a person who signed in at the sign-in at a given time.
func (f *fakeSignIn) codeAt(t *testing.T, authorize string, at time.Time, username string, groups ...string) (state, code string) {
	t.Helper()
	u, err := url.Parse(authorize)
	if err != nil || !strings.HasPrefix(authorize, signInOrigin+"/application/o/authorize/") {
		t.Fatalf("not the sign-in's authorize URL: %s", authorize)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "admin-client" || q.Get("redirect_uri") != adminOrigin+"/auth/callback" {
		t.Fatalf("authorize URL %s", authorize)
	}
	code = fmt.Sprintf("code-%d", time.Now().UnixNano())
	f.mu.Lock()
	f.codes[code] = map[string]any{"iss": issuer, "aud": "admin-client", "sub": "hashed-" + username, "preferred_username": username,
		"groups": groups, "nonce": q.Get("nonce"), "exp": f.now().Add(5 * time.Minute).Unix(), "iat": f.now().Unix(), "auth_time": at.Unix(),
		"_challenge": q.Get("code_challenge")}
	f.mu.Unlock()
	return q.Get("state"), code
}

func (f *fakeSignIn) Ready(context.Context, string, idp.Household) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notReady > 0 {
		f.notReady--
		return idp.ErrNotReady
	}
	return nil
}

func (f *fakeSignIn) Bootstrap(_ context.Context, upstream string, h idp.Household, b idp.Secret, keep func(idp.Secret) error) (idp.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if upstream != f.issuer.URL || h.SignIn() != "auth.example.test" {
		return "", fmt.Errorf("bootstrap at %s for %s", upstream, h.SignIn())
	}
	if len(f.bootstrap) > 0 {
		return "", idp.ErrTokenRefused // the token is spent
	}
	if f.beforeKeep != nil {
		f.beforeKeep()
	}
	err := keep(ownToken)
	if f.afterKeep != nil {
		f.afterKeep()
	}
	if err != nil {
		f.unkept++
		return "", err // as Authentik's adapter: the bootstrap token still works
	}
	f.bootstrap = append(f.bootstrap, b.Reveal())
	r := serveAgent(f.t, f.agentSock, "GET", "/v1/edge")
	var s agent.EdgeStatus
	_ = json.Unmarshal(r.Body, &s)
	f.servedAtBootstrap = append(f.servedAtBootstrap, s.SignIn)
	return ownToken, nil
}

func (f *fakeSignIn) Open(upstream string, h idp.Household, token idp.Secret, landing string) (idp.Provider, error) {
	if token != ownToken || landing != adminOrigin+"/" || h.Origin() != signInOrigin {
		return nil, fmt.Errorf("opened with another token, at %s, landing %s", h.Origin(), landing)
	}
	return f, nil
}

func (f *fakeSignIn) Report(context.Context) (idp.Report, error) {
	return idp.Report{Provider: "fake"}, nil
}
func (f *fakeSignIn) Prepare(context.Context, idp.Household) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	return nil
}
func (f *fakeSignIn) EnsurePerson(_ context.Context, p idp.Person) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.people[p.Username] = p
	return nil
}
func (f *fakeSignIn) People(_ context.Context, r idp.Role) ([]idp.Person, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []idp.Person
	for _, p := range f.people {
		if p.Role == r {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeSignIn) Passkeys(_ context.Context, u string) ([]idp.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.people[u]; !ok {
		return nil, idp.ErrNoPerson
	}
	return make([]idp.Passkey, f.passkeys[u]), nil
}
func (f *fakeSignIn) EnrolmentLink(_ context.Context, u string) (idp.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.people[u]; !ok {
		return idp.Link{}, idp.ErrNoPerson
	}
	l := fmt.Sprintf("%s/if/flow/homerslab-passkey-enrolment/?itoken=LINK-PLACEHOLDER-%d", signInOrigin, len(f.links))
	f.links = append(f.links, l)
	return idp.Link{URL: idp.Secret(l), ExpiresAt: time.Now().Add(idp.EnrolmentLife)}, nil
}
func (f *fakeSignIn) EnsureClient(_ context.Context, c idp.Client) (idp.ClientCredentials, error) {
	if c.Slug != "homerslab-admin" || !c.PasskeyOnly || len(c.Roles) != 1 || c.Roles[0] != idp.Admin ||
		len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != adminOrigin+"/auth/callback" {
		return idp.ClientCredentials{}, fmt.Errorf("Admin's client: %+v", c)
	}
	return idp.ClientCredentials{ID: "admin-client", Secret: clientSecret, Issuer: issuer}, nil
}

// atJoin is a primary whose first half is done: Authentik chosen, NetBird's cloud joined (household port 9443), the
// certificate issued, mail and the kit stood in.
func atJoin(t *testing.T) (*primary, *browser, *fakeSignIn) {
	t.Helper()
	fs := newFakeSignIn(t)
	nb, fd := &fakeNetBird{cloudPort: 9443}, newFakeDomains()
	p := newPrimary(t, withBuiltSteps("mail", "kit"), withNetBird(nb), withDomains(fd), func(p *primary) {
		p.signIn, p.signInUpstream = fs, fs.issuer.URL
	})
	fs.agentSock, fs.now = p.agent, p.clock.Now
	nb.now = p.clock.Now
	b := p.claimed()
	b.firstThree()
	b.mustAnswer("signin", map[string]string{"provider": "authentik"})
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	if r := p.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("the mesh's token: %d %s", r.Code, r.Body)
	}
	throughDomain(t, p, b)
	b.mustAnswer("mail", nil)
	if s := b.mustAnswer("kit", nil); s.Half != "mesh" || s.Current != "join" {
		t.Fatalf("after the first half: %s %s", s.Half, s.Current)
	}
	return p, b, fs
}

type handOver struct {
	DeviceKey          string    `json:"deviceKey"`
	DeviceKeyExpiresAt time.Time `json:"deviceKeyExpiresAt"`
	Enrolment          string    `json:"enrolment"`
	EnrolmentExpiresAt time.Time `json:"enrolmentExpiresAt"`
	SignIn             string    `json:"signIn"`
	Admin              string    `json:"admin"`
	Username           string    `json:"username"`
}

// admin is a browser on the mesh, at Admin through the edge, keeping Admin's cookies as a browser would.
type admin struct {
	t       *testing.T
	p       *primary
	cookies map[string]*http.Cookie
}

func (p *primary) adminBrowser(t *testing.T) *admin {
	return &admin{t: t, p: p, cookies: map[string]*http.Cookie{}}
}

func (a *admin) do(method, path string, edit ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, adminOrigin+path, nil)
	req.RemoteAddr = "@" // the edge, on Admin's socket
	for _, c := range a.cookies {
		req.AddCookie(c)
	}
	for _, e := range edit {
		e(req)
	}
	rec := httptest.NewRecorder()
	a.p.m.Admin().ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly {
			a.t.Errorf("Admin set the cookie %s: want __Host-, Secure, HttpOnly, Path=/ and no Domain (#83)", c)
		}
		if c.MaxAge < 0 {
			delete(a.cookies, c.Name)
		} else {
			a.cookies[c.Name] = c
		}
	}
	return rec
}

// signIn goes from Admin to the sign-in and back, as the person username in groups.
func (a *admin) signIn(fs *fakeSignIn, username string, groups ...string) *httptest.ResponseRecorder {
	a.t.Helper()
	rec := a.do("GET", "/auth/sign-in")
	if rec.Code != http.StatusSeeOther {
		a.t.Fatalf("GET /auth/sign-in: %d %s", rec.Code, rec.Body)
	}
	state, code := fs.code(a.t, rec.Header().Get("Location"), username, groups...)
	return a.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code))
}

// finish answers setup's last step from Admin's page, as the page does: from the step's revision it last read.
func (a *admin) finish() *httptest.ResponseRecorder {
	a.t.Helper()
	var s struct {
		Steps []struct {
			ID       string
			Revision int64
		}
	}
	if err := json.Unmarshal(a.do("GET", "/api/v1/account/setup").Body.Bytes(), &s); err != nil {
		a.t.Fatal(err)
	}
	rev := int64(-1)
	for _, st := range s.Steps {
		if st.ID == "done" {
			rev = st.Revision
		}
	}
	return a.do("PUT", "/api/v1/account/setup/steps/done", sameOrigin, func(r *http.Request) { r.Header.Set("If-Match", fmt.Sprintf(`"%d"`, rev)) })
}

// sameOrigin is a change from Admin's own page: JSON, from Admin's origin.
func sameOrigin(r *http.Request) {
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", adminOrigin)
	r.Header.Set("Content-Type", "application/json")
}

// First-time setup's second half, up to the admin's first sign-in over the mesh (#17):
//   - `sudo homerslab setup join` sets up the household account and hands over, in the terminal only, a key for the
//     admin's device and a one-time link for their first passkey (#64, #84);
//   - Authentik is bootstrapped before the edge serves its name, and the bootstrap token is forgotten;
//   - Admin signs in with the household account, admins only, and the first sign-in closes the LAN listener for good.
func TestTheSecondHalfEndsAtTheAdminsFirstSignInOverTheMesh(t *testing.T) {
	testenv.Isolate(t)
	p, b, fs := atJoin(t)
	fs.notReady = 2

	if r := p.control("POST", "/api/v1/setup/passkey", nil); r.Code != http.StatusConflict {
		t.Errorf("a second passkey before the first: %d %s", r.Code, r.Body)
	}
	if s := p.edge(t); s.SignIn != "" {
		t.Fatal("the edge serves the sign-in before the household account is set up")
	}
	r := p.control("POST", "/api/v1/setup/join", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	var h handOver
	r.json(t, &h)
	if !strings.HasPrefix(h.DeviceKey, "device-key-PLACEHOLDER-admin-") || !strings.HasPrefix(h.Enrolment, signInOrigin+"/if/flow/homerslab-passkey-enrolment/") ||
		h.SignIn != signInOrigin || h.Admin != adminOrigin || h.Username != "ines" {
		t.Errorf("the hand-over: %+v", h)
	}
	if d := time.Until(h.EnrolmentExpiresAt); d > idp.EnrolmentLife || d < idp.EnrolmentLife-time.Minute {
		t.Errorf("the enrolment link lasts %v", d)
	}

	t.Run("Authentik was bootstrapped before its name was served, with the token the agent sealed for it", func(t *testing.T) {
		if len(fs.bootstrap) != 1 || len(fs.servedAtBootstrap) != 1 || fs.servedAtBootstrap[0] != "" {
			t.Errorf("bootstraps %d, the edge served %q at the time", len(fs.bootstrap), fs.servedAtBootstrap)
		}
		if s := p.edge(t); s.SignIn != "auth.example.test" {
			t.Errorf("after setup join, the edge serves the sign-in as %q", s.SignIn)
		}
		r := serveAgent(t, p.agent, "GET", "/v1/sign-in")
		var s agent.SignInStatus
		r.json(t, &s)
		if s.Provider != "authentik" || s.Bootstrapping {
			t.Errorf("the agent's sign-in: %+v: the bootstrap token must be gone", s)
		}
		if p.unseal(t, "manager-sign-in-bootstrap") != "" {
			t.Error("the manager still keeps the bootstrap token")
		}
		if p.unseal(t, "manager-sign-in-token") != ownToken || p.unseal(t, "manager-admin-client-secret") != clientSecret {
			t.Error("HomersLab's token and Admin's client secret aren't sealed")
		}
		if fs.people["ines"].Role != idp.Admin || fs.people["ines"].Mail != "ines@ferreira.family" {
			t.Errorf("the first admin: %+v", fs.people["ines"])
		}
	})

	t.Run("no secret in the database or on the LAN page", func(t *testing.T) {
		secrets := []string{ownToken, clientSecret, fs.bootstrap[0], h.Enrolment, h.DeviceKey, strings.TrimPrefix(h.Enrolment, signInOrigin)}
		assertNoSecretAt(t, p, secrets...)
		page := b.do("GET", "/api/v1/setup", nil)
		for _, s := range secrets {
			if strings.Contains(string(page.Body), s) {
				t.Errorf("the LAN page's state carries a secret (%.12s…)", s)
			}
		}
	})

	a := p.adminBrowser(t)
	if rec := a.do("GET", "/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/auth/sign-in" {
		t.Errorf("Admin, signed out: %d %v", rec.Code, rec.Header())
	}

	t.Run("a sign-in that isn't this browser's, or someone who isn't an admin, gets no session", func(t *testing.T) {
		other := p.adminBrowser(t)
		rec := other.do("GET", "/auth/sign-in")
		state, code := fs.code(t, rec.Header().Get("Location"), "ines", "homerslab-admins")
		stranger := p.adminBrowser(t) // the attacker's browser, sent the admin's code (login CSRF)
		if rec := stranger.do("GET", "/auth/callback?state="+state+"&code="+code); rec.Code != http.StatusForbidden {
			t.Errorf("a callback without this browser's attempt: %d", rec.Code)
		}
		if rec := other.do("GET", "/auth/callback?state=forged&code="+code); rec.Code != http.StatusForbidden {
			t.Errorf("a forged state: %d", rec.Code)
		}
		if rec := other.do("GET", "/auth/callback?state="+state+"&code="+code); rec.Code != http.StatusForbidden {
			t.Errorf("an attempt used twice: %d", rec.Code)
		}
		late := p.adminBrowser(t)
		rec = late.do("GET", "/auth/sign-in")
		p.clock.Add(11 * time.Minute) // a sign-in left open in a tab, then finished
		state, code = fs.code(t, rec.Header().Get("Location"), "ines", "homerslab-admins")
		if rec := late.do("GET", "/auth/callback?state="+state+"&code="+code); rec.Code != http.StatusForbidden {
			t.Errorf("an attempt older than ten minutes: %d", rec.Code)
		}
		adult := p.adminBrowser(t)
		if rec := adult.signIn(fs, "rui", "homerslab-adults"); rec.Code != http.StatusForbidden {
			t.Errorf("an adult signing in to Admin: %d %s", rec.Code, rec.Body)
		}
		if _, ok := adult.cookies["__Host-homerslab-session"]; ok {
			t.Error("an adult got an Admin session")
		}
		if s := b.state(); s.Current != "join" {
			t.Errorf("a refused sign-in moved setup on: %s", s.Current)
		}
	})

	// The admin scans both codes: their device joins the mesh, and the link makes their passkey.
	fs.passkeys["ines"] = 1
	rec := a.signIn(fs, "ines", "homerslab-admins")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account/" {
		t.Fatalf("the admin's first sign-in: %d %s, want setup's last step", rec.Code, rec.Header())
	}
	if _, ok := a.cookies["__Host-homerslab-session"]; !ok {
		t.Fatal("no session after signing in")
	}

	t.Run("the first sign-in closes the setup link's LAN listener for good", func(t *testing.T) {
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusGone {
			t.Errorf("the LAN page after the first sign-in: %d", r.Code)
		}
		r := serveAgent(t, p.agent, "GET", "/v1/firewall")
		var fw agent.FirewallStatus
		r.json(t, &fw)
		if fw.SetupListener.Open {
			t.Error("the firewall table still lets the LAN reach the setup listener")
		}
		p.restart()
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusGone {
			t.Errorf("after a restart: %d", r.Code)
		}
		if r := p.control("POST", "/api/v1/setup/links", nil); r.Code != http.StatusConflict {
			t.Errorf("a new link after the LAN closed: %d", r.Code)
		}
		var s struct{ Steps []struct{ ID, State string } }
		rec := a.do("GET", "/api/v1/account/setup")
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		for _, st := range s.Steps {
			if (st.ID == "join" || st.ID == "passkey") && st.State != "done" {
				t.Errorf("%s: %s", st.ID, st.State)
			}
		}
	})

	t.Run("Admin refuses cross-site changes (#83)", func(t *testing.T) {
		for name, edit := range map[string]func(*http.Request){
			"another app of the household (same-site)": func(r *http.Request) {
				r.Header.Set("Sec-Fetch-Site", "same-site")
				r.Header.Set("Origin", "https://photos.example.test:9443")
			},
			"a page of another site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			"the session cookie with another Origin": func(r *http.Request) {
				r.Header.Set("Sec-Fetch-Site", "same-origin")
				r.Header.Set("Origin", "https://photos.example.test:9443")
			},
			"the session cookie with no Origin": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") },
			"Admin's name over plain HTTP":      func(r *http.Request) { sameOrigin(r); r.Header.Set("Origin", "http://admin.example.test:9443") },
		} {
			asJSON := func(r *http.Request) { r.Header.Set("Content-Type", "application/json"); edit(r) }
			if rec := a.do("PUT", "/api/v1/account/setup/steps/done", asJSON); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body)
			}
		}
		// A form post, which a page of another site can make without asking, is never a change.
		form := func(r *http.Request) {
			sameOrigin(r)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if rec := a.do("PUT", "/api/v1/account/setup/steps/done", form); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("a form: %d %s, want 415", rec.Code, rec.Body)
		}
		// Without a session cookie, the same rules come first; then the session check.
		cli := p.adminBrowser(t)
		if rec := cli.do("PUT", "/api/v1/account/setup/steps/done", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }); rec.Code != http.StatusForbidden {
			t.Errorf("a same-site request without a cookie: %d, want 403", rec.Code)
		}
		if rec := cli.do("PUT", "/api/v1/account/setup/steps/done", sameOrigin); rec.Code != http.StatusUnauthorized {
			t.Errorf("Admin's page, signed out: %d, want 401", rec.Code)
		}
	})

	t.Run("setup finishes only with two ways in (#87)", func(t *testing.T) {
		if rec := a.finish(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "one-way-in") {
			t.Errorf("done with one passkey: %d %s", rec.Code, rec.Body)
		}
		r := p.control("POST", "/api/v1/setup/passkey", nil)
		var again handOver
		r.json(t, &again)
		if r.Code != http.StatusOK || again.Enrolment == "" || again.Enrolment == h.Enrolment || again.DeviceKey != "" {
			t.Errorf("a second passkey's link: %d %+v", r.Code, again)
		}
		fs.passkeys["ines"] = 2
		// Like every answer, from the view of the step it was made from (#110).
		if rec := a.do("PUT", "/api/v1/account/setup/steps/done", sameOrigin); rec.Code != http.StatusPreconditionRequired {
			t.Errorf("done without the step's revision: %d %s, want 428", rec.Code, rec.Body)
		}
		if rec := a.do("PUT", "/api/v1/account/setup/steps/done", sameOrigin, func(r *http.Request) { r.Header.Set("If-Match", `"99"`) }); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stale") {
			t.Errorf("done from an older view of the step: %d %s, want 409 stale", rec.Code, rec.Body)
		}
		if rec := a.finish(); rec.Code != http.StatusOK {
			t.Errorf("done with two passkeys: %d %s", rec.Code, rec.Body)
		}
		if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusConflict {
			t.Errorf("setup join after setup: %d", r.Code)
		}
	})

	if len(fs.bootstrap) != 1 {
		t.Errorf("Authentik was bootstrapped %d times", len(fs.bootstrap))
	}
}

// Admin's sign-in is a fresh one every time: the passkey, not a session the person (or a thief) holds at the sign-in
// already, which the provider's other ways in could have made (review of #17).
func TestAdminAsksForAFreshSignInEveryTime(t *testing.T) {
	testenv.Isolate(t)
	p, _, fs := atJoin(t)
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	fs.passkeys["ines"] = 1
	a := p.adminBrowser(t)
	rec := a.do("GET", "/auth/sign-in")
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Query().Get("max_age") != "30" {
		t.Errorf("the authorize URL %s: want max_age=30", rec.Header().Get("Location"))
	}
	state, code := fs.codeAt(t, rec.Header().Get("Location"), p.clock.Now().Add(-10*time.Minute), "ines", "homerslab-admins")
	if rec := a.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code)); rec.Code != http.StatusForbidden {
		t.Errorf("a sign-in on a session from before the attempt: %d, want 403", rec.Code)
	}
	if _, ok := a.cookies["__Host-homerslab-session"]; ok {
		t.Error("an older session at the sign-in opened Admin")
	}
	if rec := a.signIn(fs, "ines", "homerslab-admins"); rec.Code != http.StatusSeeOther {
		t.Errorf("a fresh sign-in: %d %s", rec.Code, rec.Body)
	}
}

// The device key has done its work once the admin signs in over the mesh: the first sign-in ends it, so a copy of
// it (a photo of the terminal) joins nothing for the rest of its day (review of #17).
func TestTheFirstSignInEndsTheDeviceKey(t *testing.T) {
	testenv.Isolate(t)
	p, _, fs := atJoin(t)
	nb := p.netbird.(*fakeNetBird)
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	fs.passkeys["ines"] = 1
	if rec := p.adminBrowser(t).signIn(fs, "ines", "homerslab-admins"); rec.Code != http.StatusSeeOther {
		t.Fatalf("the first sign-in: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(nb.revokedKeys, " ") != "dk-1" {
		t.Errorf("revoked %v, want the device key handed over", nb.revokedKeys)
	}
}

// Asked again, setup join makes a new device key and ends the one shown before (review of #17).
func TestSetupJoinAgainEndsTheEarlierDeviceKey(t *testing.T) {
	testenv.Isolate(t)
	p, _, _ := atJoin(t)
	nb := p.netbird.(*fakeNetBird)
	for range 3 {
		if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
			t.Fatalf("setup join: %d %s", r.Code, r.Body)
		}
	}
	if strings.Join(nb.revokedKeys, " ") != "dk-1 dk-2" {
		t.Errorf("revoked %v, want the two earlier keys", nb.revokedKeys)
	}
}

// A second admin who has joined with a passkey is the other way in (#87).
func TestASecondAdminWithAPasskeyIsTheOtherWayIn(t *testing.T) {
	testenv.Isolate(t)
	p, _, fs := atJoin(t)
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	fs.passkeys["ines"] = 1
	a := p.adminBrowser(t)
	a.signIn(fs, "ines", "homerslab-admins")
	fs.people["rui"] = idp.Person{Username: "rui", Role: idp.Admin}
	if rec := a.finish(); rec.Code != http.StatusConflict {
		t.Errorf("a second admin without a passkey: %d", rec.Code)
	}
	fs.passkeys["rui"] = 1
	if rec := a.finish(); rec.Code != http.StatusOK {
		t.Errorf("a second admin with a passkey: %d %s", rec.Code, rec.Body)
	}
}

// A setup join that stopped while Authentik was starting resumes with the same bootstrap token, and asking again
// never bootstraps twice.
func TestSettingUpTheHouseholdAccountResumes(t *testing.T) {
	testenv.Isolate(t)
	p, _, fs := atJoin(t)
	fs.notReady = 1 << 30 // never starts, within the wait
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusBadGateway {
		t.Fatalf("a provider that never starts: %d %s", r.Code, r.Body)
	}
	kept := p.unseal(t, "manager-sign-in-bootstrap")
	if kept == "" {
		t.Fatal("the bootstrap token wasn't kept for the next try")
	}
	if s := p.edge(t); s.SignIn != "" {
		t.Error("the edge serves the sign-in of a provider that wasn't bootstrapped")
	}
	fs.notReady = 0
	p.restart()
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("again: %d %s", r.Code, r.Body)
	}
	if len(fs.bootstrap) != 1 || fs.bootstrap[0] != kept {
		t.Errorf("bootstrapped with %d tokens, the kept one: %v", len(fs.bootstrap), len(fs.bootstrap) == 1 && fs.bootstrap[0] == kept)
	}
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK || len(fs.bootstrap) != 1 {
		t.Errorf("a third time: %d, bootstraps %d", r.Code, len(fs.bootstrap))
	}
}

// HomersLab's new token is sealed before the bootstrap token stops working: a seal that fails leaves the bootstrap
// token working, and the next try bootstraps with it (review of #17).
func TestANewTokenThatCouldntBeKeptIsNeverTheOnlyOne(t *testing.T) {
	testenv.Isolate(t)
	p, _, fs := atJoin(t)
	fs.beforeKeep = func() { p.host.Fail("systemd-creds encrypt") }
	fs.afterKeep = func() { p.host.Mend() }
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusBadGateway {
		t.Fatalf("a token that couldn't be sealed: %d %s", r.Code, r.Body)
	}
	if fs.unkept != 1 || len(fs.bootstrap) != 0 {
		t.Fatalf("unkept %d, bootstraps %d", fs.unkept, len(fs.bootstrap))
	}
	fs.beforeKeep, fs.afterKeep = nil, nil
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("again: %d %s", r.Code, r.Body)
	}
	if p.unseal(t, "manager-sign-in-token") != ownToken || len(fs.bootstrap) != 1 {
		t.Error("the second try didn't keep HomersLab's token")
	}
}

// A renewal, or the certificate put on the edge again, keeps the sign-in on the edge (review of #17).
func TestTheEdgeKeepsTheSignInWhenTheCertificateIsPutOnItAgain(t *testing.T) {
	testenv.Isolate(t)
	p, _, _ := atJoin(t)
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	if r := p.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code != http.StatusOK {
		t.Fatalf("the DNS token again: %d %s", r.Code, r.Body)
	}
	if s := p.edge(t); s.SignIn != "auth.example.test" || s.Admin != "admin.example.test" {
		t.Errorf("after the certificate went on the edge again: %+v", s)
	}
}

var _ manager.SignIn = (*fakeSignIn)(nil)
