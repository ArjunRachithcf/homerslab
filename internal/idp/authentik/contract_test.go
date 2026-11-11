package authentik_test

// The identity provider contract suite against a real Authentik, run from HomersLab's own packaged compose file
// (packaging/sign-in/authentik.compose.yaml) as compose project hltest-17, its addresses moved to the test's own
// range. It needs Docker: it runs when HOMERSLAB_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise.
// The code under test never sees that engine (testenv points DOCKER_HOST at nowhere); only this harness does,
// explicitly.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
	"github.com/ArjunRachithcf/homerslab/internal/idp/idptest"
	"github.com/ArjunRachithcf/homerslab/internal/idp/webauthntest"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const (
	project = "hltest-17"
	// The test's own range for the sign-in network, instead of the package's 172.31.250.0/28.
	upstream = "http://10.217.0.10:9000"
	landing  = "https://admin.example.test/"
	redirect = "https://admin.example.test/auth/callback"
)

var household = idp.Household{Name: "The Ferreiras", Domain: "example.test", Port: 443}

func TestTheIdentityProviderContractOnAuthentik(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to the engine for the contract suite's containers")
	}
	w := up(t, engine)
	ctx := context.Background()

	t.Run("claim (#84): Authentik's WebAuthn stages don't require user verification unless told to", func(t *testing.T) {
		// Reported, not verified (spec: Testing Decisions): why HomersLab's stages set it.
		type setup struct {
			Name string `json:"name"`
			UV   string `json:"user_verification"`
		}
		type validate struct {
			Name string `json:"name"`
			UV   string `json:"webauthn_user_verification"`
		}
		var s struct{ Results []setup }
		var v struct{ Results []validate }
		w.raw(t, w.bootstrap, "GET", "/api/v3/stages/authenticator/webauthn/", nil, &s)
		w.raw(t, w.bootstrap, "GET", "/api/v3/stages/authenticator/validate/", nil, &v)
		var fresh setup
		w.raw(t, w.bootstrap, "POST", "/api/v3/stages/authenticator/webauthn/", map[string]any{"name": "hltest-fresh"}, &fresh)
		t.Logf("the default enrolment stages: %+v; validation stages: %+v; a new stage with no setting: %q", s.Results, v.Results, fresh.UV)
		if fresh.UV == "required" {
			t.Errorf("the claim doesn't hold: a new WebAuthn stage requires user verification by itself")
		}
		for _, x := range s.Results {
			if x.UV == "required" {
				t.Errorf("the claim doesn't hold: %s requires user verification", x.Name)
			}
		}
	})

	var own idp.Secret
	t.Run("bootstrap: HomersLab's own account from Authentik's bootstrap token, which then stops working", func(t *testing.T) {
		var err error
		for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(3 * time.Second) {
			own, err = authentik.Bootstrap(ctx, upstream, household, w.bootstrap, func(idp.Secret) error { return nil })
			if !errors.Is(err, idp.ErrNotReady) || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if own == "" || own == w.bootstrap {
			t.Fatal("no token of HomersLab's own")
		}
		if code := w.status(t, w.bootstrap, "/api/v3/core/users/me/"); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("the bootstrap token after bootstrap: %d, want refused", code)
		}
		var ak struct {
			Results []struct {
				IsActive bool `json:"is_active"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/core/users/?username=akadmin", nil, &ak)
		if len(ak.Results) != 1 || ak.Results[0].IsActive {
			t.Errorf("akadmin: %+v, want there and inactive", ak.Results)
		}
		if _, err := authentik.Bootstrap(ctx, upstream, household, w.bootstrap, func(idp.Secret) error { return nil }); !errors.Is(err, idp.ErrTokenRefused) {
			t.Errorf("bootstrapping again with the spent token: %v, want refused", err)
		}
	})
	if own == "" {
		t.Fatal("no token: nothing else can be checked")
	}

	t.Run("a restart keeps the bootstrap token dead, even if the unit still handed it over", func(t *testing.T) {
		w.compose(t, "restart", "server")
		w.ready(t)
		if code := w.status(t, w.bootstrap, "/api/v3/core/users/me/"); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("the bootstrap token after a restart: %d, want refused", code)
		}
		if code := w.status(t, own, "/api/v3/core/users/me/"); code != http.StatusOK {
			t.Errorf("HomersLab's token after a restart: %d", code)
		}
	})

	a, err := authentik.Open(upstream, household, own, landing)
	if err != nil {
		t.Fatal(err)
	}
	w.own = own
	w.provider = a
	idptest.Run(t, w, authentik.Version)

	t.Run("in a real browser: Chromium's virtual authenticator enrols at the sign-in's own name and signs in to Admin (#83, #84)", func(t *testing.T) {
		if os.Getenv("HOMERSLAB_TEST_BROWSER") == "" {
			t.Skip("set HOMERSLAB_TEST_BROWSER to run web/e2e/passkey.idp.spec.ts in Chromium (CI's identity job does)")
		}
		inABrowser(t, w, a)
	})

	t.Run("the enrolment flow is HomersLab's own: only an invitation and a passkey, never the recovery flow (#84, #87)", func(t *testing.T) {
		var brands struct {
			Results []struct {
				Domain   string  `json:"domain"`
				Recovery *string `json:"flow_recovery"`
				SignIn   *string `json:"flow_authentication"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/core/brands/?domain="+household.SignIn(), nil, &brands)
		if len(brands.Results) != 1 || brands.Results[0].Recovery != nil || brands.Results[0].SignIn == nil {
			t.Errorf("the sign-in's brand: %+v, want HomersLab's sign-in and no recovery flow", brands.Results)
		}
		var flows struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/flows/instances/?slug="+authentik.EnrolmentFlow, nil, &flows)
		var bindings struct {
			Results []struct {
				Stage struct {
					Component string `json:"component"`
				} `json:"stage_obj"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/flows/bindings/?ordering=order&target="+flows.Results[0].PK, nil, &bindings)
		var got []string
		for _, b := range bindings.Results {
			got = append(got, b.Stage.Component)
		}
		want := []string{"ak-stage-invitation-form", "ak-stage-deny-form", "ak-stage-authenticator-webauthn-form", "ak-stage-redirect-form"}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("the enrolment flow's stages: %v, want %v (the deny stage refuses any link that isn't valid)", got, want)
		}
	})
}

// inABrowser runs the browser's side, web/e2e/passkey.idp.spec.ts, against this Authentik behind a TLS stand-in for
// the edge, which Chromium trusts by its key alone. Chromium makes the passkey from a fresh enrolment link and signs in
// to Admin's client; the code it brings back is exchanged and its ID token verified here.
func inABrowser(t *testing.T, w *world, a *authentik.Authentik) {
	ctx := context.Background()
	if err := a.EnsurePerson(ctx, idp.Person{Username: "marta", Name: "Marta", Mail: "marta@ferreira.family", Role: idp.Admin}); err != nil {
		t.Fatal(err)
	}
	link, err := a.EnrolmentLink(ctx, "marta")
	if err != nil {
		t.Fatal(err)
	}
	c := w.OIDC()
	creds, err := a.EnsureClient(ctx, idp.Client{Slug: "homerslab-admin", Name: "Admin", RedirectURIs: []string{redirect}, Roles: []idp.Role{idp.Admin}, PasskeyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Issuer, c.ID, c.Secret = creds.Issuer, creds.ID, creds.Secret
	e, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := oidc.NewAttempt()

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(17), Subject: pkix.Name{CommonName: "auth.example.test"},
		DNSNames: []string{"auth.example.test", "admin.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(spki)
	up, _ := url.Parse(upstream)
	proxy := httputil.NewSingleHostReverseProxy(up)
	edge := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "admin.example.test") {
			fmt.Fprint(rw, "Admin, at "+r.URL.Path) // the landing and the callback: the URL is what counts
			return
		}
		r.Header.Set("X-Forwarded-Proto", "https")
		proxy.ServeHTTP(rw, r)
	}))
	edge.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	edge.StartTLS()
	defer edge.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "callback.txt")
	spec, _ := json.Marshal(map[string]string{"address": strings.TrimPrefix(edge.URL, "https://"), "spki": base64.StdEncoding.EncodeToString(sum[:]),
		"enrolment": link.URL.Reveal(), "authorize": c.AuthorizeURL(e, at, false), "username": "marta", "redirect": redirect,
		"landing": landing, "out": out})
	if err := os.WriteFile(filepath.Join(dir, "world.json"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HOMERSLAB_TEST_BROWSER") == "external" {
		// The browser runs elsewhere (a developer's machine, where Go runs in a container): wait for its answer.
		t.Logf("waiting for the browser: HOMERSLAB_TEST_SIGNIN=%s npx playwright test e2e/passkey.idp.spec.ts --project=desktop", filepath.Join(dir, "world.json"))
		for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if _, err := os.Stat(out); err == nil {
				break
			}
		}
	} else {
		_, self, _, _ := runtime.Caller(0)
		cmd := exec.Command("npx", "playwright", "test", "e2e/passkey.idp.spec.ts", "--project=desktop", "--reporter=list")
		cmd.Dir = filepath.Join(filepath.Dir(self), "..", "..", "..", "web")
		cmd.Env = append(os.Environ(), "HOMERSLAB_TEST_SIGNIN="+filepath.Join(dir, "world.json"))
		got, err := cmd.CombinedOutput()
		t.Logf("the browser:\n%s", got)
		if err != nil {
			t.Fatalf("the browser's side failed: %v", err)
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := url.Parse(strings.TrimSpace(string(b)))
	cl, err := c.Finish(ctx, e, at, cb.Query().Get("state"), cb.Query().Get("code"), time.Now())
	if err != nil {
		t.Fatalf("the browser's code: %v", err)
	}
	if cl.Username != "marta" || !slices.Contains(cl.Groups, idp.RoleGroup(idp.Admin)) {
		t.Errorf("claims %+v", cl)
	}
}

// world is the compose project, and the suite's browser.
type world struct {
	engine, dir string
	bootstrap   idp.Secret
	own         idp.Secret
	provider    *authentik.Authentik
}

func (w *world) Provider() idp.Provider   { return w.provider }
func (w *world) Household() idp.Household { return household }
func (w *world) OIDC() oidc.Client {
	return oidc.Client{Redirect: redirect, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: oidc.Through(household.Origin(), upstream)}}
}

func secret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// up starts the packaged compose file, with the test's addresses and secret files, and waits until Authentik takes
// its bootstrap token.
func up(t *testing.T, engine string) *world {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	packaged, err := os.ReadFile(filepath.Join(filepath.Dir(self), "..", "..", "..", "packaging", "sign-in", "authentik.compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w := &world{engine: engine, dir: dir, bootstrap: idp.Secret(secret())}
	secrets := map[string]string{"secret-key": secret() + secret(), "postgres-password": "", "bootstrap-token": w.bootstrap.Reveal()}
	secrets["postgres-password"] = secret()
	secrets["postgres-password-db"] = secrets["postgres-password"]
	for name, v := range secrets {
		// The unit hands each file to its container user alone (0400); the test can't chown, so they are readable by
		// all, in a folder of the test's own.
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		// As the unit does: the sign-in's own SELinux level.
		args := []string{"-t", "container_file_t", "-l", "s0:c1000,c1001"}
		for name := range secrets {
			args = append(args, filepath.Join(dir, name))
		}
		if out, err := exec.Command("chcon", args...).CombinedOutput(); err != nil {
			t.Fatalf("labelling the secrets: %v %s", err, out)
		}
	}
	file := strings.NewReplacer("name: homerslab-authentik", "name: "+project, "172.31.250.", "10.217.0.",
		"/run/homerslab-authentik/", dir+"/").Replace(string(packaged))
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	w.composeQuiet("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.composeOut("logs", "--no-color", "--tail", "80")
			t.Logf("the containers' logs:\n%s", out)
		}
		if os.Getenv("HOMERSLAB_TEST_KEEP") == "" {
			w.composeQuiet("down", "-v", "--remove-orphans")
		}
	})
	w.compose(t, "up", "-d", "--quiet-pull")
	w.ready(t)
	deadline := time.Now().Add(3 * time.Minute)
	for w.status(t, w.bootstrap, "/api/v3/core/users/me/") != http.StatusOK || w.status(t, w.bootstrap, "/api/v3/flows/instances/default-provider-authorization-implicit-consent/") != http.StatusOK || w.status(t, w.bootstrap, "/api/v3/flows/instances/default-authenticator-webauthn-setup/") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("Authentik never took its bootstrap token, or never made its default flows")
		}
		time.Sleep(2 * time.Second)
	}
	return w
}

func (w *world) ready(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for authentik.Ready(context.Background(), upstream, household) != nil {
		if time.Now().After(deadline) {
			t.Fatal("Authentik never became ready")
		}
		time.Sleep(2 * time.Second)
	}
}

func (w *world) cmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", filepath.Join(w.dir, "compose.yaml")}, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
	return cmd
}

func (w *world) composeOut(args ...string) (string, error) {
	out, err := w.cmd(args...).CombinedOutput()
	return string(out), err
}

func (w *world) composeQuiet(args ...string) { _, _ = w.composeOut(args...) }

func (w *world) compose(t *testing.T, args ...string) {
	t.Helper()
	if out, err := w.composeOut(args...); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// client reaches the sign-in's origin at the upstream, as the edge would, with its own cookies.
func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 30 * time.Second, Jar: jar, Transport: oidc.Through(household.Origin(), upstream),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// raw calls Authentik's API with a token, for what the suite checks behind the adapter's back.
func (w *world) raw(t *testing.T, token idp.Secret, method, path string, body, out any) {
	t.Helper()
	code, b := w.do(t, token, method, path, body)
	if code/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

func (w *world) status(t *testing.T, token idp.Secret, path string) int {
	code, _ := w.do(t, token, "GET", path, nil)
	return code
}

func (w *world) do(t *testing.T, token idp.Secret, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, household.URL(path), r)
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	req.Header.Set("Content-Type", "application/json")
	resp, err := client().Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (w *world) userPK(t *testing.T, username string) int {
	var us struct {
		Results []struct {
			PK int `json:"pk"`
		}
	}
	w.raw(t, w.own, "GET", "/api/v3/core/users/?username="+url.QueryEscape(username), nil, &us)
	if len(us.Results) != 1 {
		t.Fatalf("no user %s", username)
	}
	return us.Results[0].PK
}

func (w *world) Expire(t *testing.T, username string) {
	var inv struct {
		Results []struct {
			PK        string         `json:"pk"`
			FixedData map[string]any `json:"fixed_data"`
		}
	}
	w.raw(t, w.own, "GET", "/api/v3/stages/invitation/invitations/?flow__slug="+authentik.EnrolmentFlow, nil, &inv)
	for _, i := range inv.Results {
		if i.FixedData["homerslab_person"] == username {
			w.raw(t, w.own, "PATCH", "/api/v3/stages/invitation/invitations/"+i.PK+"/",
				map[string]any{"expires": time.Now().Add(-time.Second).UTC().Format(time.RFC3339)}, nil)
		}
	}
}

func (w *world) Drift(t *testing.T) {
	for _, s := range []struct{ path, field string }{{"/api/v3/stages/authenticator/webauthn/", "user_verification"},
		{"/api/v3/stages/authenticator/validate/", "webauthn_user_verification"}} {
		var l struct {
			Results []struct {
				PK   string `json:"pk"`
				Name string `json:"name"`
			}
		}
		w.raw(t, w.own, "GET", s.path, nil, &l)
		for _, x := range l.Results {
			if strings.HasPrefix(x.Name, "homerslab-") {
				w.raw(t, w.own, "PATCH", s.path+x.PK+"/", map[string]string{s.field: "preferred"}, nil)
			}
		}
	}
}

func (w *world) GivePassword(t *testing.T, username string) string {
	pw := secret()
	w.raw(t, w.own, "POST", fmt.Sprintf("/api/v3/core/users/%d/set_password/", w.userPK(t, username)), map[string]string{"password": pw}, nil)
	return pw
}

// FirstAdmin tries Authentik's first-admin paths as a stranger: the setup page, the first-admin flow's page, and its
// executor API, answering its prompt with a password of the stranger's. It then tries to sign in as akadmin with
// that password through Authentik's own default flow. Each path that let the stranger in is named.
func (w *world) FirstAdmin(t *testing.T) []string {
	var in []string
	stranger := secret()
	c := client()
	// The setup page and the flow's own page, as a browser opens them: they may plan the flow in this session.
	for _, p := range []string{"/", "/if/flow/initial-setup/"} {
		resp, err := c.Get(household.URL(p))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	ch := w.executor(t, c, "initial-setup", "", nil)
	if ch["component"] == "ak-stage-prompt" {
		in = append(in, "the executor shows the first-admin prompt")
		ch = w.executor(t, c, "initial-setup", "", map[string]any{"component": "ak-stage-prompt", "email": "x@example.org",
			"password": stranger, "password_repeat": stranger, "base_url": household.Origin()})
		t.Logf("the first-admin flow took the stranger's answer: %v", ch["component"])
	}
	// Whatever the flow said: does akadmin open with the stranger's password now?
	b := client()
	ch = w.executor(t, b, "default-authentication-flow", "", nil)
	if ch["component"] == "ak-stage-identification" {
		ch = w.executor(t, b, "default-authentication-flow", "", map[string]any{"component": "ak-stage-identification", "uid_field": "akadmin", "password": stranger})
		if ch["component"] == "ak-stage-password" {
			ch = w.executor(t, b, "default-authentication-flow", "", map[string]any{"component": "ak-stage-password", "password": stranger})
		}
	}
	resp, err := b.Get(household.URL("/api/v3/core/users/me/"))
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"username":"akadmin"`) {
			in = append(in, "akadmin with the stranger's password")
		}
	}
	return in
}

// executor gets the flow executor's current challenge, or posts a response to it.
func (w *world) executor(t *testing.T, c *http.Client, slug, query string, response map[string]any) map[string]any {
	t.Helper()
	u := household.URL("/api/v3/flows/executor/" + slug + "/?query=" + url.QueryEscape(query))
	var req *http.Request
	if response == nil {
		req, _ = http.NewRequest("GET", u, nil)
	} else {
		b, _ := json.Marshal(response)
		req, _ = http.NewRequest("POST", u, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", household.Origin())
		req.Header.Set("Referer", household.URL("/if/flow/"+slug+"/"))
		base, _ := url.Parse(household.Origin())
		for _, ck := range c.Jar.Cookies(base) {
			if ck.Name == "authentik_csrf" {
				req.Header.Set("X-authentik-CSRF", ck.Value)
			}
		}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// The executor answers its first request by planning the flow and sending the browser back to itself.
	for range 3 {
		loc := resp.Header.Get("Location")
		if resp.StatusCode/100 != 3 || !strings.HasPrefix(loc, "/api/v3/flows/executor/") {
			break
		}
		resp.Body.Close()
		again, _ := http.NewRequest("GET", household.URL(loc), nil)
		again.Header.Set("Accept", "application/json")
		if resp, err = c.Do(again); err != nil {
			t.Fatal(err)
		}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var ch map[string]any
	if err := json.Unmarshal(b, &ch); err != nil {
		return map[string]any{"component": fmt.Sprintf("http-%d", resp.StatusCode), "body": string(b), "location": resp.Header.Get("Location")}
	}
	return ch
}

func (w *world) Browser(t *testing.T, a *webauthntest.Authenticator) idptest.Browser {
	return &browser{w: w, c: client(), a: a}
}

type browser struct {
	w *world
	c *http.Client
	a *webauthntest.Authenticator
}

func (b *browser) Enrol(t *testing.T, link idp.Link) (idptest.Enrolment, error) {
	u, err := url.Parse(link.URL.Reveal())
	if err != nil || !strings.HasPrefix(u.String(), household.Origin()+"/if/flow/") {
		return idptest.Enrolment{}, fmt.Errorf("not a link at the sign-in's origin: %v", err)
	}
	slug := strings.Trim(strings.TrimPrefix(u.Path, "/if/flow/"), "/")
	resp, err := b.c.Get(u.String())
	if err != nil {
		return idptest.Enrolment{}, err
	}
	resp.Body.Close()
	var e idptest.Enrolment
	ch := b.w.executor(t, b.c, slug, u.RawQuery, nil)
	for range 10 {
		switch ch["component"] {
		case "ak-stage-authenticator-webauthn":
			raw, _ := json.Marshal(ch["registration"])
			var o webauthntest.CreationOptions
			if err := json.Unmarshal(raw, &o); err != nil {
				return e, err
			}
			e = idptest.Enrolment{RPID: o.RP.ID, UserVerification: o.AuthenticatorSelection.UserVerification}
			cred, err := b.a.Create(o)
			if err != nil {
				return e, err
			}
			ch = b.w.executor(t, b.c, slug, u.RawQuery, map[string]any{"component": "ak-stage-authenticator-webauthn", "response": cred})
		case "xak-flow-redirect":
			if e.RPID == "" {
				return e, fmt.Errorf("the flow ended without a passkey, at %v", ch["to"])
			}
			if ch["to"] != landing {
				return e, fmt.Errorf("the enrolment ended at %v, want %s", ch["to"], landing)
			}
			return e, nil
		default:
			return e, fmt.Errorf("the enrolment flow answered %v: %v", ch["component"], ch)
		}
	}
	return e, errors.New("the enrolment flow never ended")
}

func (b *browser) SignIn(t *testing.T, authorize, username, password string) (string, string, error) {
	next := authorize
	for range 20 {
		req, _ := http.NewRequest("GET", next, nil)
		resp, err := b.c.Do(req)
		if err != nil {
			return "", "", err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		switch {
		case resp.StatusCode/100 == 3 && strings.HasPrefix(loc, redirect):
			u, _ := url.Parse(loc)
			if e := u.Query().Get("error"); e != "" {
				return "", "", fmt.Errorf("the client was refused: %s", e)
			}
			return u.Query().Get("state"), u.Query().Get("code"), nil
		case resp.StatusCode/100 == 3:
			u, _ := url.Parse(next)
			l, _ := u.Parse(loc)
			next = l.String()
			continue
		}
		u, _ := url.Parse(next)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(u.Path, "/if/flow/") {
			return "", "", fmt.Errorf("GET %s: %d", u.Path, resp.StatusCode)
		}
		slug := strings.Trim(strings.TrimPrefix(u.Path, "/if/flow/"), "/")
		to, err := b.flow(t, slug, u.RawQuery, username, password)
		if err != nil {
			return "", "", err
		}
		l, _ := u.Parse(to)
		next = l.String()
	}
	return "", "", errors.New("the sign-in never came back to the client")
}

// flow runs one flow to its end, and returns where it sends the browser.
func (b *browser) flow(t *testing.T, slug, query, username, password string) (string, error) {
	ch := b.w.executor(t, b.c, slug, query, nil)
	for range 10 {
		switch ch["component"] {
		case "ak-stage-identification":
			r := map[string]any{"component": "ak-stage-identification", "uid_field": username}
			if password != "" {
				r["password"] = password
			}
			ch = b.w.executor(t, b.c, slug, query, r)
		case "ak-stage-password":
			if password == "" {
				return "", errors.New("the flow asked for a password")
			}
			ch = b.w.executor(t, b.c, slug, query, map[string]any{"component": "ak-stage-password", "password": password})
		case "ak-stage-authenticator-validate":
			raw, _ := json.Marshal(ch["device_challenges"])
			var dcs []struct {
				Class     string          `json:"device_class"`
				Challenge json.RawMessage `json:"challenge"`
			}
			_ = json.Unmarshal(raw, &dcs)
			var resp map[string]any
			for _, dc := range dcs {
				if dc.Class != "webauthn" {
					continue
				}
				var o webauthntest.RequestOptions
				if err := json.Unmarshal(dc.Challenge, &o); err != nil {
					return "", err
				}
				cred, err := b.a.Get(o)
				if err != nil {
					return "", err
				}
				resp = map[string]any{"component": "ak-stage-authenticator-validate", "webauthn": cred}
			}
			if resp == nil {
				return "", fmt.Errorf("the flow offered no passkey: %v", dcs)
			}
			ch = b.w.executor(t, b.c, slug, query, resp)
			if ch["component"] == "ak-stage-authenticator-validate" {
				return "", fmt.Errorf("the passkey was refused: %v", ch["response_errors"])
			}
		case "xak-flow-redirect":
			to, _ := ch["to"].(string)
			return to, nil
		default:
			return "", fmt.Errorf("the flow %s answered %v: %v", slug, ch["component"], ch)
		}
	}
	return "", errors.New("the flow never ended")
}
