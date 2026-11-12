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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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
	"regexp"
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
		// Bootstrap, like Prepare, goes on only once Authentik has made every flow Prepare guards: one made later
		// would never be guarded.
		for _, slug := range authentik.DefaultFlows {
			if code := w.status(t, own, "/api/v3/flows/instances/"+slug+"/"); code != http.StatusOK {
				t.Errorf("the default flow %s, once bootstrap was done: %d", slug, code)
			}
		}
		var bs struct {
			Results []struct {
				Path, Status string
			}
		}
		w.raw(t, own, "GET", "/api/v3/managed/blueprints/?page_size=500", nil, &bs)
		for _, b := range bs.Results {
			if (strings.HasPrefix(b.Path, "default/") || strings.HasPrefix(b.Path, "system/")) && b.Status != "successful" {
				t.Errorf("the blueprint %s, once bootstrap was done: %s", b.Path, b.Status)
			}
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

	t.Run("drift: the report reads the household policy's expression too, and Prepare puts it back", func(t *testing.T) {
		var ps struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/policies/expression/?name=homerslab-household-person", nil, &ps)
		if len(ps.Results) != 1 {
			t.Fatalf("the household policy: %+v", ps.Results)
		}
		w.raw(t, own, "PATCH", "/api/v3/policies/expression/"+ps.Results[0].PK+"/", map[string]string{"expression": "return False"}, nil)
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a household policy that knows nobody: %+v %v, want PasskeyOnlySignIn false", r, err)
		}
		if err := a.Prepare(ctx, household); err != nil {
			t.Fatal(err)
		}
		if r, _ := a.Report(ctx); !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare: %+v", r)
		}
	})

	t.Run("in a real browser: Chromium's virtual authenticator enrols at the sign-in's own name and signs in to Admin (#83, #84)", func(t *testing.T) {
		if os.Getenv("HOMERSLAB_TEST_BROWSER") == "" {
			t.Skip("set HOMERSLAB_TEST_BROWSER to run web/e2e/passkey.idp.spec.ts in Chromium (CI's identity job does)")
		}
		inABrowser(t, w, a)
	})

	t.Run("Authentik's logs, which reach the journal, never hold an enrolment link's token", func(t *testing.T) {
		out, err := w.composeOut("logs", "--no-color", "server", "worker")
		if err != nil {
			t.Fatalf("docker compose logs: %v", err)
		}
		if m := regexp.MustCompile(`itoken(=|%3D|%253D)[0-9a-fA-F-]{8,}`).FindString(out); m != "" {
			t.Errorf("a log line holds an enrolment link's token (%d bytes of logs): %.20s…", len(out), m)
		}
		if !strings.Contains(out, "Starting authentik server") && !strings.Contains(out, "authentik") {
			t.Errorf("no logs to check")
		}
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

	t.Run("the unit's compose command ends when one container stops, so systemd starts it again", func(t *testing.T) {
		// The packaged unit's own command, on the test's project and file, attached to the running containers. Last: it
		// stops them all.
		_, self, _, _ := runtime.Caller(0)
		u, err := os.ReadFile(filepath.Join(filepath.Dir(self), "..", "..", "..", "packaging", "systemd", "homerslab-authentik.service"))
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for _, l := range strings.Split(string(u), "\n") {
			if rest, ok := strings.CutPrefix(l, "ExecStart=/usr/bin/docker compose "); ok {
				args = strings.Fields(rest)
			}
		}
		for i := range args {
			switch args[i] {
			case "homerslab-authentik":
				args[i] = project
			case "/usr/share/homerslab/sign-in/authentik.compose.yaml":
				args[i] = filepath.Join(w.dir, "compose.yaml")
			}
		}
		if len(args) == 0 {
			t.Fatal("no ExecStart in the unit")
		}
		up := exec.Command("docker", append([]string{"compose"}, args...)...)
		up.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
		var out strings.Builder
		up.Stdout, up.Stderr = &out, &out
		if err := up.Start(); err != nil {
			t.Fatal(err)
		}
		ended := make(chan error, 1)
		go func() { ended <- up.Wait() }()
		time.Sleep(10 * time.Second) // attached
		w.compose(t, "kill", "worker")
		select {
		case <-ended:
		case <-time.After(90 * time.Second):
			_ = up.Process.Kill()
			<-ended
			t.Errorf("the worker stopped, and the unit's command still runs: systemd would never start it again")
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
	at := oidc.FreshAttempt(time.Now()) // as Admin asks

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
		"enrolment": link.URL.Reveal(), "authorize": c.AuthorizeURL(e, at), "username": "marta", "redirect": redirect,
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
	// Only until it takes its bootstrap token: whether its default flows are made is the adapter's to wait for.
	for w.status(t, w.bootstrap, "/api/v3/core/users/me/") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("Authentik never took its bootstrap token")
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
	var brands struct {
		Results []struct {
			UUID   string `json:"brand_uuid"`
			Domain string `json:"domain"`
		}
	}
	w.raw(t, w.own, "GET", "/api/v3/core/brands/", nil, &brands)
	for _, b := range brands.Results {
		if b.Domain == household.SignIn() {
			w.raw(t, w.own, "PATCH", "/api/v3/core/brands/"+b.UUID+"/", map[string]string{"domain": "sso." + household.Domain}, nil)
		}
	}
	var f struct {
		Results []struct {
			PK string `json:"pk"`
		}
	}
	for _, slug := range []string{"default-authentication-flow", "default-authenticator-webauthn-setup"} {
		w.raw(t, w.own, "GET", "/api/v3/flows/instances/?slug="+slug, nil, &f)
		var bs struct {
			Results []struct {
				PK    string `json:"pk"`
				Stage struct {
					Name string `json:"name"`
				} `json:"stage_obj"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+f.Results[0].PK, nil, &bs)
		for _, b := range bs.Results {
			if strings.HasPrefix(b.Stage.Name, "homerslab-") {
				w.raw(t, w.own, "DELETE", "/api/v3/flows/bindings/"+b.PK+"/", nil, nil)
			}
		}
	}
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

// GiveFactors sets a password through the API and makes a TOTP device in Authentik's own shell, as an admin in its
// console could.
func (w *world) GiveFactors(t *testing.T, username string) idptest.Factors {
	t.Helper()
	pw := secret() + "-Aa1!"
	w.raw(t, w.own, "POST", fmt.Sprintf("/api/v3/core/users/%d/set_password/", w.userPK(t, username)), map[string]string{"password": pw}, nil)
	key := make([]byte, 20)
	_, _ = rand.Read(key)
	code := "from authentik.core.models import User\nfrom authentik.stages.authenticator_totp.models import TOTPDevice\n" +
		fmt.Sprintf("TOTPDevice.objects.create(user=User.objects.get(username=%q), name=\"hltest\", key=%q, confirmed=True)\n", username, hex.EncodeToString(key))
	if out, err := w.composeOut("exec", "-T", "server", "ak", "shell", "-c", code); err != nil {
		t.Fatalf("making a TOTP device: %v %s", err, out)
	}
	return idptest.Factors{Password: pw, TOTP: totp(t, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key))}
}

// SetUpAuthenticators signs in with the passkey through HomersLab's sign-in, then runs Authentik's default setup flows
// for a passkey (with a), TOTP, recovery codes and a password, and reads through the API which of them set anything up.
func (w *world) SetUpAuthenticators(t *testing.T, ib idptest.Browser, username string, a *webauthntest.Authenticator) []string {
	t.Helper()
	b := ib.(*browser)
	if _, err := b.flow(t, authentik.SignInFlow, "", username, idptest.Factors{}); err != nil {
		t.Fatalf("the passkey sign-in: %v", err)
	}
	pk := w.userPK(t, username)
	count := func(kind string) int {
		var l struct{ Results []any }
		w.raw(t, w.own, "GET", fmt.Sprintf("/api/v3/authenticators/admin/%s/?user=%d", kind, pk), nil, &l)
		return len(l.Results)
	}
	changed := func() string {
		var u struct {
			Changed string `json:"password_change_date"`
		}
		w.raw(t, w.own, "GET", fmt.Sprintf("/api/v3/core/users/%d/", pk), nil, &u)
		return u.Changed
	}
	var took []string
	for _, f := range []struct {
		slug, kind string
		answer     func(ch map[string]any) map[string]any
	}{
		{"default-authenticator-webauthn-setup", "webauthn", func(ch map[string]any) map[string]any {
			raw, _ := json.Marshal(ch["registration"])
			var o webauthntest.CreationOptions
			_ = json.Unmarshal(raw, &o)
			cred, err := a.Create(o)
			if err != nil {
				t.Fatalf("the other authenticator: %v", err)
			}
			return map[string]any{"component": "ak-stage-authenticator-webauthn", "response": cred}
		}},
		{"default-authenticator-totp-setup", "totp", func(ch map[string]any) map[string]any {
			cfg, _ := url.Parse(fmt.Sprint(ch["config_url"]))
			return map[string]any{"component": "ak-stage-authenticator-totp", "code": totp(t, cfg.Query().Get("secret"))()}
		}},
		{"default-authenticator-static-setup", "static", func(map[string]any) map[string]any {
			return map[string]any{"component": "ak-stage-authenticator-static"}
		}},
		{"default-password-change", "password", func(map[string]any) map[string]any {
			pw := secret() + "-Aa1!"
			return map[string]any{"component": "ak-stage-prompt", "password": pw, "password_repeat": pw}
		}},
	} {
		before, was := 0, changed()
		if f.kind != "password" {
			before = count(f.kind)
		}
		ch := w.executor(t, b.c, f.slug, "", nil)
		switch ch["component"] {
		case "ak-stage-access-denied":
			continue // refused
		case "ak-stage-authenticator-webauthn", "ak-stage-authenticator-totp", "ak-stage-authenticator-static", "ak-stage-prompt":
			ch = w.executor(t, b.c, f.slug, "", f.answer(ch))
		default:
			t.Fatalf("%s answered %v: %v", f.slug, ch["component"], ch)
		}
		if ch["component"] == "ak-stage-access-denied" {
			continue
		}
		if (f.kind == "password" && changed() != was) || (f.kind != "password" && count(f.kind) > before) {
			took = append(took, f.slug)
			continue
		}
		t.Fatalf("%s neither set anything up nor refused: %v", f.slug, ch)
	}
	return took
}

// ProviderSignIn runs Authentik's default authentication flow, which takes a password and TOTP. A wrong password or
// code fails the test: the factors must be taken, so a refusal is the flow's.
func (w *world) ProviderSignIn(t *testing.T, ib idptest.Browser, username string, f idptest.Factors) error {
	t.Helper()
	b := ib.(*browser)
	b.strict = true
	defer func() { b.strict = false }()
	if _, err := b.flow(t, "default-authentication-flow", "", username, f); err != nil {
		return err
	}
	resp, err := b.c.Get(household.URL("/api/v3/core/users/me/"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf("%q:%q", "username", username)) {
		return fmt.Errorf("no session: %d", resp.StatusCode)
	}
	return nil
}

// totp is RFC 6238's code maker for a base32 secret (SHA-1, 6 digits, 30 s): each call a code of a later period than
// the last, since Authentik takes a period's code only once.
func totp(t *testing.T, secret string) func() string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		t.Fatalf("the TOTP secret: %v", err)
	}
	last := int64(-1)
	return func() string {
		step := time.Now().Unix() / 30
		for step <= last {
			time.Sleep(time.Until(time.Unix((step+1)*30, 0)) + 100*time.Millisecond)
			step = time.Now().Unix() / 30
		}
		last = step
		m := hmac.New(sha1.New, key)
		_ = binary.Write(m, binary.BigEndian, step)
		sum := m.Sum(nil)
		o := sum[len(sum)-1] & 0x0f
		n := binary.BigEndian.Uint32(sum[o:o+4]) & 0x7fffffff
		return fmt.Sprintf("%06d", n%1000000)
	}
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
	// strict fails the test when a stage turns down a password or code: the harness's factors must be right.
	strict bool
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

func (b *browser) SignIn(t *testing.T, authorize, username string, f idptest.Factors) (string, string, error) {
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
		to, err := b.flow(t, slug, u.RawQuery, username, f)
		if err != nil {
			return "", "", err
		}
		l, _ := u.Parse(to)
		next = l.String()
	}
	return "", "", errors.New("the sign-in never came back to the client")
}

// flow runs one flow to its end, and returns where it sends the browser.
func (b *browser) flow(t *testing.T, slug, query, username string, f idptest.Factors) (string, error) {
	ch := b.w.executor(t, b.c, slug, query, nil)
	for range 10 {
		switch ch["component"] {
		case "ak-stage-identification":
			r := map[string]any{"component": "ak-stage-identification", "uid_field": username}
			if f.Password != "" {
				r["password"] = f.Password
			}
			ch = b.w.executor(t, b.c, slug, query, r)
		case "ak-stage-password":
			if f.Password == "" {
				return "", errors.New("the flow asked for a password")
			}
			ch = b.w.executor(t, b.c, slug, query, map[string]any{"component": "ak-stage-password", "password": f.Password})
			if b.strict && ch["component"] == "ak-stage-password" {
				t.Fatalf("the password was refused: %v", ch["response_errors"])
			}
		case "ak-stage-authenticator-validate":
			raw, _ := json.Marshal(ch["device_challenges"])
			var dcs []struct {
				Class     string          `json:"device_class"`
				Challenge json.RawMessage `json:"challenge"`
			}
			_ = json.Unmarshal(raw, &dcs)
			var resp map[string]any
			for _, dc := range dcs {
				switch {
				case dc.Class == "webauthn" && b.a.Passkeys() > 0:
					var o webauthntest.RequestOptions
					if err := json.Unmarshal(dc.Challenge, &o); err != nil {
						return "", err
					}
					cred, err := b.a.Get(o)
					if err != nil {
						return "", err
					}
					resp = map[string]any{"component": "ak-stage-authenticator-validate", "webauthn": cred}
				case dc.Class == "totp" && f.TOTP != nil && resp == nil:
					resp = map[string]any{"component": "ak-stage-authenticator-validate", "code": f.TOTP()}
				}
			}
			if resp == nil {
				return "", fmt.Errorf("the flow offered nothing this browser has: %v", dcs)
			}
			ch = b.w.executor(t, b.c, slug, query, resp)
			if ch["component"] == "ak-stage-authenticator-validate" {
				if b.strict {
					t.Fatalf("the code was refused: %v", ch["response_errors"])
				}
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
