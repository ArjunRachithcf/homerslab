package authentik_test

// The identity provider contract suite against a real Authentik, run from Homeward's own packaged compose file
// (packaging/sign-in/authentik.compose.yaml) as compose project hltest-17, its addresses moved to the test's own
// range. It needs Docker: it runs when HOMEWARD_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise.
// The code under test never sees that engine (testenv points DOCKER_HOST at nowhere); only this harness does,
// explicitly.

import (
	"cmp"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/edge"
	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/idp/authentik"
	"github.com/ArjunRachithcf/homeward/internal/idp/idptest"
	"github.com/ArjunRachithcf/homeward/internal/idp/webauthntest"
	"github.com/ArjunRachithcf/homeward/internal/oidc"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// project is the suite's compose project: hltest-17, or another of a test's own on a machine where that one may run
// beside it (HOMEWARD_TEST_IDP_PROJECT).
var project = cmp.Or(os.Getenv("HOMEWARD_TEST_IDP_PROJECT"), "hltest-17")

// signInNet is the test's own range for the sign-in network (the first three octets of a /28 at .0), instead of the
// package's 172.31.250.0/28; HOMEWARD_TEST_IDP_NET names another, for a run beside another one on the same machine.
var (
	signInNet = cmp.Or(os.Getenv("HOMEWARD_TEST_IDP_NET"), "10.217.0")
	upstream  = "http://" + signInNet + ".10:9000"
)

const (
	landing  = "https://admin.example.test/"
	redirect = "https://admin.example.test/auth/callback"
)

var household = idp.Household{Name: "The Ferreiras", Domain: "example.test", Port: 443}

func TestTheIdentityProviderContractOnAuthentik(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMEWARD_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMEWARD_TEST_DOCKER_HOST to the engine for the contract suite's containers")
	}
	w := up(t, engine)
	ctx := context.Background()

	var own idp.Secret
	t.Run("bootstrap: Homeward's own account from Authentik's bootstrap token, which then stops working", func(t *testing.T) {
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
			t.Fatal("no token of Homeward's own")
		}
		// Bootstrap, like Prepare, goes on only once Authentik has made every flow Prepare guards: one made later
		// would never be guarded.
		for _, slug := range authentik.DefaultFlows {
			if code := w.status(t, own, "/api/v3/flows/instances/"+slug+"/"); code != http.StatusOK {
				t.Errorf("the default flow %s, once bootstrap was done: %d", slug, code)
			}
		}
		// Authentik's worker may run a blueprint again after Bootstrap saw it succeed, and two of its own runs can
		// deadlock in Postgres, leaving one in error (#254): that one is applied again, as the adapter does, until a
		// deadline. One that stays in error, or that Bootstrap didn't wait for, still fails.
		list := func() []blueprint {
			var bs struct{ Results []blueprint }
			w.raw(t, own, "GET", "/api/v3/managed/blueprints/?page_size=500", nil, &bs)
			return bs.Results
		}
		apply := func(pk string) { w.do(t, own, "POST", "/api/v3/managed/blueprints/"+pk+"/apply/", nil) }
		for _, left := range settle(list, apply, 3*time.Minute, 20*time.Second) {
			t.Errorf("the blueprint %s, once bootstrap was done", left)
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

	t.Run("claim (#84): Authentik's WebAuthn stages don't require user verification unless told to", func(t *testing.T) {
		// Reported, not verified (spec: Testing Decisions): why Homeward's stages set it. Read once Authentik has
		// made its defaults (bootstrap waits for them), with Homeward's own token, and before Prepare.
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
		w.raw(t, own, "GET", "/api/v3/stages/authenticator/webauthn/?name=default-authenticator-webauthn-setup", nil, &s)
		w.raw(t, own, "GET", "/api/v3/stages/authenticator/validate/?name=default-authentication-mfa-validation", nil, &v)
		var fresh setup
		w.raw(t, own, "POST", "/api/v3/stages/authenticator/webauthn/", map[string]any{"name": "hltest-fresh"}, &fresh)
		t.Logf("the default passkey setup stage: %+v; the default validation stage: %+v; a new stage with no setting: %q", s.Results, v.Results, fresh.UV)
		if len(s.Results) != 1 || len(v.Results) != 1 {
			t.Fatalf("Authentik's default passkey stages aren't there: %+v %+v", s.Results, v.Results)
		}
		if fresh.UV == "required" {
			t.Errorf("the claim doesn't hold: a new WebAuthn stage requires user verification by itself")
		}
		if s.Results[0].UV == "required" || v.Results[0].UV == "required" {
			t.Errorf("the claim doesn't hold: Authentik's default passkey stages require user verification: %+v %+v", s.Results, v.Results)
		}
	})

	t.Run("a restart keeps the bootstrap token dead, even if the unit still handed it over", func(t *testing.T) {
		w.compose(t, "restart", "server")
		w.ready(t)
		if code := w.status(t, w.bootstrap, "/api/v3/core/users/me/"); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("the bootstrap token after a restart: %d, want refused", code)
		}
		if code := w.status(t, own, "/api/v3/core/users/me/"); code != http.StatusOK {
			t.Errorf("Homeward's token after a restart: %d", code)
		}
	})

	a, err := authentik.Open(upstream, household, own, landing)
	if err != nil {
		t.Fatal(err)
	}
	w.own = own
	w.provider = a
	idptest.Run(t, w, authentik.Version)
	t.Run("LDAP: an app's directory at the outpost, for its roles only (#204)", func(t *testing.T) {
		idptest.RunLDAP(t, w)
		t.Run("an LDAP-only app binds from its own container through the LDAPS port, and reads only its own person", ldapAppInItsContainer(w, a))
		t.Run("drift: a bind flow that takes passwords, or another app's, shows in the report, and Prepare puts it back", ldapDrift(w, a))
		t.Run("an app's directory keeps apart from the app's own application", ldapBesideTheAppsOwn(w, a))
		t.Run("drift: a provider at the outpost without its application is closed and taken off, then deleted", ldapOrphan(w, a))
		t.Run("drift: providers of the console's at the outpost, named as an app's, are taken off", ldapImpostors(w, a))
		t.Run("the outpost's own unit: its exit ends its own command, never the sign-in's", ldapOwnUnit(w))
	})

	t.Run("an app's audience changes without the app ever being open to someone of neither audience (#219)", func(t *testing.T) {
		// Inês, an admin, is of neither audience: Authentik's own access check for her runs all along, as the changes
		// are made. An application with no binding at all would let her in.
		ines := w.userPK(t, "ines")
		stop, done := make(chan struct{}), make(chan []string, 1)
		stopped := false
		defer func() {
			if !stopped {
				close(stop)
			}
		}()
		go func() {
			var let []string
			for checks := 0; ; checks++ {
				select {
				case <-stop:
					done <- append(let, fmt.Sprintf("checks: %d", checks))
					return
				default:
				}
				code, b := w.do(t, own, "GET", fmt.Sprintf("/api/v3/core/applications/homerslab-chores/check_access/?for_user=%d", ines), nil)
				var r struct {
					Passing bool `json:"passing"`
				}
				if code != http.StatusOK || json.Unmarshal(b, &r) != nil || r.Passing {
					let = append(let, fmt.Sprintf("%d %s", code, b))
				}
				time.Sleep(2 * time.Millisecond) // a check is tens of milliseconds: the window it looks for is of that order
			}
		}()
		g := idp.Guard{Slug: "homerslab-chores", Name: "Chores", URL: "https://chores.example.test"}
		for i := range 8 {
			g.Roles = []idp.Role{[]idp.Role{idp.Adult, idp.Kid}[i%2]}
			if err := w.provider.EnsureGuard(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
		close(stop)
		stopped = true
		let := <-done
		t.Log(let[len(let)-1])
		if len(let) > 1 {
			t.Errorf("while its audience changed, the app let in someone of neither, or the check failed: %v", let[:len(let)-1])
		}
	})

	t.Run("a group's name never gives a role: the guard names it from the role groups' own names (#236)", func(t *testing.T) {
		if edge.AuthentikGuard("").Role != authentik.RoleHeader {
			t.Fatalf("the edge reads the role from %s, the guard names it in %s", edge.AuthentikGuard("").Role, authentik.RoleHeader)
		}
		ctx, p := context.Background(), w.provider
		if err := p.EnsurePerson(ctx, idp.Person{Username: "theo", Name: "Theo", Mail: "theo@ferreira.family", Role: idp.Kid}); err != nil {
			t.Fatal(err)
		}
		link, err := p.EnrolmentLink(ctx, "theo")
		if err != nil {
			t.Fatal(err)
		}
		key := webauthntest.New(household.Origin())
		if _, err := w.Browser(t, key).Enrol(t, link); err != nil {
			t.Fatal(err)
		}
		chores := "chores." + household.Domain // the kids' guarded app, from the suite
		pass := func(want string) {
			t.Helper()
			status, role := w.PassGuard(t, key, "theo", chores)
			if status/100 == 2 && role != want || role == string(idp.Admin) {
				t.Errorf("theo at %s: %d, role %q, want %q", chores, status, role, want)
			}
		}
		pass("kid")
		type group struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		}
		groupOf := func(name string) group {
			var gs struct{ Results []group }
			w.raw(t, own, "GET", "/api/v3/core/groups/?name="+url.QueryEscape(name), nil, &gs)
			if len(gs.Results) != 1 {
				t.Fatalf("no group %s", name)
			}
			return gs.Results[0]
		}
		theo, kids := w.userPK(t, "theo"), groupOf(idp.RoleGroup(idp.Kid))
		// A household group whose name joins two others' with Authentik's own separator: still a kid.
		var pipe, club group
		w.raw(t, own, "POST", "/api/v3/core/groups/", map[string]any{"name": "x|" + idp.RoleGroup(idp.Admin), "users": []int{theo}}, &pipe)
		pass("kid")
		// Below the kids' group, which the kids' audience counts, and no longer in it: no role at all.
		w.raw(t, own, "POST", "/api/v3/core/groups/", map[string]any{"name": "kids-club|" + idp.RoleGroup(idp.Admin), "parents": []string{kids.PK},
			"users": []int{theo}}, &club)
		w.raw(t, own, "POST", "/api/v3/core/groups/"+kids.PK+"/remove_user/", map[string]int{"pk": theo}, nil)
		pass("")
		for _, g := range []group{pipe, club} {
			w.raw(t, own, "DELETE", "/api/v3/core/groups/"+g.PK+"/", nil, nil)
		}

		// Attributes that would set a header of Authentik's or Homeward's under another spelling (review round 2): the
		// outpost sets each, the last one winning, the person's id too. The mapping then names no role, and empties them,
		// so the edge refuses the person rather than take a forged role or id.
		w.raw(t, own, "POST", "/api/v3/core/groups/"+kids.PK+"/add_user/", map[string]int{"pk": theo}, nil)
		pass("kid")
		forged := map[string]any{"additionalHeaders": map[string]any{"x-authentik-homerslab-role": "admin", "X-AUTHENTIK-HOMERSLAB-ROLE": "admin",
			"X-Authentik-Uid": "forged-uid", "x_authentik_name": "forged"}}
		refused := func(how string) {
			t.Helper()
			status, h := w.passGuard(t, key, "theo", chores)
			uid, role := h.Get(edge.AuthentikGuard("").Person), h.Get(authentik.RoleHeader)
			if status/100 == 2 && (role != "" || uid == "forged-uid" || strings.Contains(h.Get("X-Authentik-Name"), "forged")) {
				t.Errorf("%s: %d, role %q, uid %q, name %q: want no role, and nothing forged", how, status, role, uid, h.Get("X-Authentik-Name"))
			}
		}
		w.raw(t, own, "PATCH", fmt.Sprintf("/api/v3/core/users/%d/", theo), map[string]any{"attributes": forged}, nil)
		refused("in the person's attributes")
		w.raw(t, own, "PATCH", fmt.Sprintf("/api/v3/core/users/%d/", theo), map[string]any{"attributes": map[string]any{}}, nil)
		pass("kid")
		var house group
		w.raw(t, own, "POST", "/api/v3/core/groups/", map[string]any{"name": "the-house", "users": []int{theo}, "attributes": forged}, &house)
		refused("in a group's attributes")
		w.raw(t, own, "DELETE", "/api/v3/core/groups/"+house.PK+"/", nil, nil)
		pass("kid")
	})

	t.Run("the role mapping names no role when anything is forged, whatever order the outpost writes in (#244)", roleMappingFailsClosed(w))
	t.Run("drift: a foreign scope mapping on a guard, or Homeward's role mapping changed, shows in the report, and Prepare puts it back (#244)", guardDrift(w, a))

	t.Run("each guard's provider keeps its own app's URL (#219)", func(t *testing.T) {
		for slug, host := range map[string]string{"homerslab-bentopdf": "https://pdf.example.test", "homerslab-chores": "https://chores.example.test"} {
			var ps struct {
				Results []struct {
					Name string `json:"name"`
					Host string `json:"external_host"`
					App  string `json:"assigned_application_slug"`
				}
			}
			w.raw(t, own, "GET", "/api/v3/providers/proxy/?name__iexact="+slug, nil, &ps)
			if len(ps.Results) != 1 || ps.Results[0].Name != slug || ps.Results[0].Host != host || ps.Results[0].App != slug {
				t.Errorf("the provider %s: %+v, want one at %s for its own application", slug, ps.Results, host)
			}
		}
	})

	// Authentik applies its own blueprints again when its worker likes (at its start, after an upgrade, hourly). Its
	// first-admin blueprint must apply over Homeward's closing of that flow, and the flow stay closed (#195's CI:
	// the sign-in waited on that blueprint, which had failed at Authentik's first start).
	t.Run("Authentik applies its first-admin blueprint again: it succeeds, and the flow still applies to nobody (#84)", func(t *testing.T) {
		var bs struct {
			Results []struct {
				PK     string `json:"pk"`
				Path   string `json:"path"`
				Status string `json:"status"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/managed/blueprints/?path=default/flow-oobe.yaml", nil, &bs)
		if len(bs.Results) != 1 {
			t.Fatalf("the first-admin blueprint: %+v", bs.Results)
		}
		w.raw(t, own, "POST", "/api/v3/managed/blueprints/"+bs.Results[0].PK+"/apply/", nil, nil)
		status := ""
		for range 60 {
			w.raw(t, own, "GET", "/api/v3/managed/blueprints/?path=default/flow-oobe.yaml", nil, &bs)
			if status = bs.Results[0].Status; status == "successful" {
				break
			}
			time.Sleep(time.Second)
		}
		if status != "successful" {
			t.Errorf("the first-admin blueprint, applied again: %s", status)
		}
		if in := w.FirstAdmin(t); len(in) > 0 {
			t.Errorf("after the blueprint again, a stranger became an admin through: %v", in)
		}
		if r, err := a.Report(ctx); err != nil || !r.FirstAdminOff {
			t.Errorf("the report: %+v %v", r, err)
		}
	})

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
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, _ := a.Report(ctx); !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare: %+v", r)
		}
	})

	t.Run("in a real browser: Chromium's virtual authenticator enrols at the sign-in's own name and signs in to Admin (#83, #84)", func(t *testing.T) {
		if os.Getenv("HOMEWARD_TEST_BROWSER") == "" {
			t.Skip("set HOMEWARD_TEST_BROWSER to run web/e2e/passkey.idp.spec.ts in Chromium (CI's identity job does)")
		}
		inABrowser(t, w, a)
	})

	// Installing an app makes its client in one call to Authentik (#20; API research: one call per app): the provider,
	// the application and the bindings that limit it to its audience, in one transaction, so a failure leaves none of
	// them. Its secret never reaches Authentik's logs.
	t.Run("an app's client and the policy limiting it to its audience: one write, Authentik's transactional call (#20)", func(t *testing.T) {
		var mu sync.Mutex
		var writes []string
		up, _ := url.Parse(upstream)
		proxy := httputil.NewSingleHostReverseProxy(up)
		rec := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				mu.Lock()
				writes = append(writes, r.Method+" "+r.URL.Path)
				mu.Unlock()
			}
			proxy.ServeHTTP(rw, r)
		}))
		defer rec.Close()
		counted, err := authentik.Open(rec.URL, household, own, landing)
		if err != nil {
			t.Fatal(err)
		}
		c := idp.Client{Slug: "homerslab-notes", Name: "Notes", RedirectURIs: []string{"https://notes.example.test/callback"}, Roles: []idp.Role{idp.Adult}}
		creds, err := counted.EnsureClient(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"PUT /api/v3/core/transactional/applications/"}; !slices.Equal(writes, want) {
			t.Errorf("making the client wrote %v, want %v", writes, want)
		}
		// Exact redirect addresses: every one strict, never a pattern (regex mode would take https://notesXexample.test).
		var provs struct {
			Results []struct {
				Redirects []struct {
					Mode string `json:"matching_mode"`
					URL  string `json:"url"`
				} `json:"redirect_uris"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/providers/oauth2/?name=homerslab-notes", nil, &provs)
		if len(provs.Results) != 1 || len(provs.Results[0].Redirects) != 1 {
			t.Fatalf("the client's provider: %+v", provs.Results)
		}
		for _, r := range provs.Results[0].Redirects {
			if r.Mode != "strict" || r.URL != c.RedirectURIs[0] {
				t.Errorf("a redirect address %q, matched %q: want strict", r.URL, r.Mode)
			}
		}
		var app struct {
			PBM string `json:"pbm_uuid"`
		}
		w.raw(t, own, "GET", "/api/v3/core/applications/homerslab-notes/", nil, &app)
		var bs struct {
			Results []struct {
				Group string `json:"group"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/policies/bindings/?target="+app.PBM, nil, &bs)
		if len(bs.Results) != 1 || bs.Results[0].Group == "" {
			t.Errorf("the application's bindings: %+v, want the adults' group alone", bs.Results)
		}
		mu.Lock()
		writes = nil
		mu.Unlock()
		if _, err := counted.EnsureClient(ctx, c); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(writes, "PUT /api/v3/core/transactional/applications/") {
			t.Errorf("an existing client was made again: %v", writes)
		}
		if err := counted.DeleteClient(ctx, c.Slug); err != nil {
			t.Fatal(err)
		}
		if code := w.status(t, own, "/api/v3/core/applications/homerslab-notes/"); code != http.StatusNotFound {
			t.Errorf("the deleted client's application: %d", code)
		}
		// Never another kind's: a guard's application (the contract's BentoPDF, of the same form of slug) stays, and
		// Admin's and Home's clients are kept.
		if code := w.status(t, own, "/api/v3/core/applications/homerslab-bentopdf/"); code != http.StatusOK {
			t.Fatalf("the contract's guard: %d", code)
		}
		if err := counted.DeleteClient(ctx, "homerslab-bentopdf"); err == nil {
			t.Error("a guard's application was taken for a client's")
		}
		if code := w.status(t, own, "/api/v3/core/applications/homerslab-bentopdf/"); code != http.StatusOK {
			t.Errorf("the guard's application, after a client's deleting: %d", code)
		}
		for _, kept := range []string{"homerslab-admin", "homerslab-home"} {
			if err := counted.DeleteClient(ctx, kept); err == nil {
				t.Errorf("DeleteClient took %s", kept)
			}
		}
		out, err := w.composeOut("logs", "--no-color", "server", "worker")
		if err != nil {
			t.Fatalf("docker compose logs: %v", err)
		}
		if strings.Contains(out, creds.Secret.Reveal()) {
			t.Error("Authentik's logs hold an app client's secret")
		}
	})

	// Stories 27 and 32 (#20): a person signed in to the household account opens an app with native sign-in (Mealie)
	// and is in it, with no second login, in Chromium with its virtual authenticator (web/e2e/native-sign-in.idp.spec.ts).
	// Mealie's server reaches the sign-in only through the edge's sign-in listener (#19, ADR 0020): native_test.go.
	t.Run("in a real browser: a signed-in person opens an app with native sign-in, with no second login (#20)", func(t *testing.T) {
		if os.Getenv("HOMEWARD_TEST_BROWSER") == "" {
			t.Skip("set HOMEWARD_TEST_BROWSER to run web/e2e/native-sign-in.idp.spec.ts in Chromium (CI's identity job does)")
		}
		nativeSignIn(t, w, a)
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

	t.Run("the enrolment flow is Homeward's own: only an invitation and a passkey, never the recovery flow (#84, #87)", func(t *testing.T) {
		var brands struct {
			Results []struct {
				Domain   string  `json:"domain"`
				Recovery *string `json:"flow_recovery"`
				SignIn   *string `json:"flow_authentication"`
			}
		}
		w.raw(t, own, "GET", "/api/v3/core/brands/?domain="+household.SignIn(), nil, &brands)
		if len(brands.Results) != 1 || brands.Results[0].Recovery != nil || brands.Results[0].SignIn == nil {
			t.Errorf("the sign-in's brand: %+v, want Homeward's sign-in and no recovery flow", brands.Results)
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
		u, err := os.ReadFile(filepath.Join(filepath.Dir(self), "..", "..", "..", "packaging", "systemd", "homeward-authentik.service"))
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
			case "/usr/share/homeward/sign-in/authentik.compose.yaml":
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
//
// HOMEWARD_TEST_BROWSER_RUNS runs the browser's side that many times in a row (CI's manual run proves a flake gone
// with 20). Each run is a person of its own, with an enrolment link and an authorize attempt of its own: an enrolment
// link works once, and a run never reuses what another used up.
func inABrowser(t *testing.T, w *world, a *authentik.Authentik) {
	ctx := context.Background()
	runs := 1
	if n, err := strconv.Atoi(os.Getenv("HOMEWARD_TEST_BROWSER_RUNS")); err == nil && n > 1 {
		runs = n
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

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(17), Subject: pkix.Name{CommonName: "auth.example.test"},
		DNSNames: []string{"auth.example.test", "admin.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(2 * time.Hour),
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

	passed := 0
	for run := 1; run <= runs; run++ {
		username := "marta"
		if run > 1 {
			username = fmt.Sprintf("marta%d", run)
		}
		if err := browserRun(t, a, c, e, edge.URL, base64.StdEncoding.EncodeToString(sum[:]), username, run); err != nil {
			t.Errorf("browser run %d of %d: %v", run, runs, err)
			continue
		}
		passed++
	}
	if runs > 1 {
		t.Logf("browser runs: %d of %d passed", passed, runs)
	}
}

// browserRun is one run of web/e2e/passkey.idp.spec.ts, for a new person: their own enrolment link, a fresh authorize
// attempt, and its own folder for what Playwright keeps of a failure (web/test-results/passkey-<run>).
func browserRun(t *testing.T, a *authentik.Authentik, c oidc.Client, e oidc.Endpoints, edgeURL, spki, username string, run int) error {
	t.Helper()
	ctx := context.Background()
	if err := a.EnsurePerson(ctx, idp.Person{Username: username, Name: "Marta", Mail: username + "@ferreira.family", Role: idp.Admin}); err != nil {
		return err
	}
	link, err := a.EnrolmentLink(ctx, username)
	if err != nil {
		return err
	}
	at := oidc.FreshAttempt(time.Now()) // as Admin asks
	su := oidc.StepUpAttempt(time.Now(), fmt.Sprintf("plan-browser-run-%d", run))

	dir := t.TempDir()
	out, outStepUp := filepath.Join(dir, "callback.txt"), filepath.Join(dir, "step-up.txt")
	spec, _ := json.Marshal(map[string]string{"address": strings.TrimPrefix(edgeURL, "https://"), "spki": spki,
		"enrolment": link.URL.Reveal(), "authorize": c.AuthorizeURL(e, at), "username": username, "redirect": redirect,
		"landing": landing, "out": out, "stepUp": c.AuthorizeURL(e, su), "outStepUp": outStepUp})
	if err := os.WriteFile(filepath.Join(dir, "world.json"), spec, 0o600); err != nil {
		return err
	}
	if os.Getenv("HOMEWARD_TEST_BROWSER") == "external" {
		// The browser runs elsewhere (a developer's machine, where Go runs in a container): wait for its answer.
		shareWithTheBrowser(t, dir, filepath.Join(dir, "world.json"))
		t.Logf("waiting for the browser: HOMEWARD_TEST_SIGNIN=%s npx playwright test e2e/passkey.idp.spec.ts --project=desktop", filepath.Join(dir, "world.json"))
		for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if _, err := os.Stat(out); err == nil {
				break
			}
		}
	} else {
		_, self, _, _ := runtime.Caller(0)
		cmd := exec.Command("npx", "playwright", "test", "e2e/passkey.idp.spec.ts", "--project=desktop", "--reporter=list",
			fmt.Sprintf("--output=test-results/passkey-%d", run))
		cmd.Dir = filepath.Join(filepath.Dir(self), "..", "..", "..", "web")
		cmd.Env = append(os.Environ(), "HOMEWARD_TEST_SIGNIN="+filepath.Join(dir, "world.json"))
		got, err := cmd.CombinedOutput()
		t.Logf("the browser, run %d:\n%s", run, got)
		if err != nil {
			return fmt.Errorf("the browser's side failed: %v", err)
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	cb, _ := url.Parse(strings.TrimSpace(string(b)))
	cl, err := c.Finish(ctx, e, at, cb.Query().Get("state"), cb.Query().Get("code"), time.Now())
	if err != nil {
		return fmt.Errorf("the browser's code: %v", err)
	}
	if cl.Username != username || !slices.Contains(cl.Groups, idp.RoleGroup(idp.Admin)) {
		return fmt.Errorf("claims %+v", cl)
	}
	// The step-up's code: its own nonce, and a sign-in later than the one its session began with (#25).
	b, err = os.ReadFile(outStepUp)
	if err != nil {
		return err
	}
	cb, _ = url.Parse(strings.TrimSpace(string(b)))
	up, err := c.Finish(ctx, e, su, cb.Query().Get("state"), cb.Query().Get("code"), time.Now())
	if err != nil {
		return fmt.Errorf("the step-up's code: %v", err)
	}
	if up.Username != username || !up.AuthTime.After(cl.AuthTime) {
		return fmt.Errorf("the step-up signed in %s at %v, the session at %v: want a new sign-in", up.Username, up.AuthTime, cl.AuthTime)
	}
	return nil
}

// world is the compose project, and the suite's browser.
type world struct {
	engine, dir string
	bootstrap   idp.Secret
	own         idp.Secret
	provider    *authentik.Authentik
}

// AskGuard is the edge's forward-auth request to Authentik's embedded outpost, for a request to host.
func (w *world) AskGuard(t *testing.T, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", upstream+"/outpost.goauthentik.io/auth/caddy", nil)
	for k, v := range map[string]string{"X-Forwarded-Host": host, "X-Forwarded-Proto": "https", "X-Forwarded-Uri": "/", "X-Forwarded-Method": "GET"} {
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// PassGuard goes to host in a browser holding a's passkeys, as username, the way the guard sends it: to the outpost's
// start, the sign-in, back to the app's callback; then it asks the guard for host with the session it came back with.
// A person the app's application refuses never gets that far: the answer is then the guard's to a browser with none.
func (w *world) PassGuard(t *testing.T, a *webauthntest.Authenticator, username, host string) (int, string) {
	t.Helper()
	status, h := w.passGuard(t, a, username, host)
	return status, h.Get(authentik.RoleHeader)
}

// passGuard is PassGuard, with the whole of the guard's answer's headers.
func (w *world) passGuard(t *testing.T, a *webauthntest.Authenticator, username, host string) (int, http.Header) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Timeout: 30 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		// Every name of the household's goes to Authentik, under its own Host, as the edge sends it.
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" || !strings.HasSuffix(r.URL.Hostname(), "."+household.Domain) {
				return nil, fmt.Errorf("%s isn't the household's", r.URL.Redacted())
			}
			out := r.Clone(r.Context())
			u, _ := url.Parse(upstream)
			out.URL.Scheme, out.URL.Host, out.Host = u.Scheme, u.Host, r.URL.Host
			out.Header.Set("X-Forwarded-Proto", "https")
			return http.DefaultTransport.RoundTrip(out)
		})}
	b := &browser{w: w, c: c, a: a}
	next := "https://" + host + "/outpost.goauthentik.io/start?rd=" + url.QueryEscape("https://"+host+"/")
	for step := 0; ; step++ {
		if step == 20 {
			t.Fatalf("%s at %s: the sign-in never came back to the app nor refused, at %s", username, host, next)
		}
		u, _ := url.Parse(next)
		if u.Host == host && u.Path == "/" { // back at the app: the session is the browser's
			break
		}
		resp, err := c.Get(next)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && u.Path == "/application/o/authorize/" {
			// Signed in, and the application's policies refuse the person: Authentik answers its own denial page.
			t.Logf("%s at %s: refused by the application", username, host)
			break
		}
		if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 {
			l, _ := u.Parse(loc)
			if l.Host == "localhost" {
				// The embedded outpost sends the browser to Authentik at its own idea of its host (its configuration's
				// authentik_host_browser, unset): the sign-in's origin is where the edge serves it.
				o, _ := url.Parse(household.Origin())
				l.Scheme, l.Host = o.Scheme, o.Host
			}
			next = l.String()
			continue
		}
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(u.Path, "/if/flow/") {
			t.Fatalf("GET %s%s: %d", u.Host, u.Path, resp.StatusCode)
		}
		to, err := b.flow(t, strings.Trim(strings.TrimPrefix(u.Path, "/if/flow/"), "/"), u.RawQuery, username, idptest.Factors{})
		if errors.Is(err, errDenied) {
			t.Logf("%s at %s: refused by the sign-in", username, host)
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		l, _ := u.Parse(to)
		next = l.String()
	}
	req, _ := http.NewRequest("GET", upstream+"/outpost.goauthentik.io/auth/caddy", nil)
	for k, v := range map[string]string{"X-Forwarded-Host": host, "X-Forwarded-Proto": "https", "X-Forwarded-Uri": "/", "X-Forwarded-Method": "GET"} {
		req.Header.Set(k, v)
	}
	for _, ck := range jar.Cookies(&url.URL{Scheme: "https", Host: host, Path: "/"}) {
		req.AddCookie(ck)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// errDenied is a flow that ended in Authentik's access-denied stage: a policy refused the person.
var errDenied = errors.New("access denied")

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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

// blueprint is one of Authentik's blueprint instances, as its API lists them.
type blueprint struct {
	PK     string `json:"pk"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// settle looks at Authentik's default and system blueprints until each has applied (or is orphaned: an upgrade
// removed its file), and returns, as "path: status", those that haven't by the end of wait. One in error is applied
// again, not more often than every; one in warning, or in any other state at the first look (Bootstrap didn't wait for
// it), is returned at once.
func settle(list func() []blueprint, apply func(pk string), wait, every time.Duration) []string {
	deadline := time.Now().Add(wait)
	applied := map[string]time.Time{}
	for first := true; ; first = false {
		var left, early []string
		for _, b := range list() {
			if !strings.HasPrefix(b.Path, "default/") && !strings.HasPrefix(b.Path, "system/") || b.Status == "successful" || b.Status == "orphaned" {
				continue
			}
			left = append(left, b.Path+": "+b.Status)
			if b.Status != "error" {
				if first || b.Status == "warning" {
					early = append(early, b.Path+": "+b.Status)
				}
				continue
			}
			if time.Since(applied[b.PK]) >= every {
				apply(b.PK)
				applied[b.PK] = time.Now()
			}
		}
		if len(early) > 0 {
			return early
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(min(every, 2*time.Second))
	}
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
	// The outpost's token is empty until an app signs in over LDAP (StartOutpost), as the unit leaves it.
	secrets := map[string]string{"secret-key": secret() + secret(), "postgres-password": "", "bootstrap-token": w.bootstrap.Reveal(), "ldap-token": ""}
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
	file := strings.NewReplacer("name: homerslab-authentik", "name: "+project, "172.31.250.", signInNet+".",
		"/run/homeward-authentik/", dir+"/", "/run/homeward-ldap/", dir+"/").Replace(string(packaged))
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	w.composeQuiet("--profile", "ldap", "down", "-v", "--remove-orphans") // the outpost too
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.composeOut("logs", "--no-color", "--tail", "80")
			t.Logf("the containers' logs:\n%s", out)
		}
		if os.Getenv("HOMEWARD_TEST_KEEP") == "" {
			w.composeQuiet("--profile", "ldap", "down", "-v", "--remove-orphans") // the outpost too
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

// GiveOthers makes an app password through the API, and recovery codes (a static device) in Authentik's own shell.
func (w *world) GiveOthers(t *testing.T, username string) {
	t.Helper()
	w.raw(t, w.own, "POST", "/api/v3/core/tokens/", map[string]any{"identifier": "hltest-app-password-" + username, "intent": "app_password",
		"user": w.userPK(t, username), "expiring": false}, nil)
	code := "from authentik.core.models import User\nfrom authentik.stages.authenticator_static.models import StaticDevice, StaticToken\n" +
		fmt.Sprintf("d = StaticDevice.objects.create(user=User.objects.get(username=%q), name=\"hltest\", confirmed=True)\n", username) +
		"StaticToken.objects.create(device=d, token=\"hltest00\")\n"
	if out, err := w.composeOut("exec", "-T", "server", "ak", "shell", "-c", code); err != nil {
		t.Fatalf("making recovery codes: %v %s", err, out)
	}
}

// SetUpAuthenticators signs in with the passkey through Homeward's sign-in, then runs Authentik's default setup flows
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
	// The client's redirect address, as the authorize URL asks for it: where the browser stops.
	au, err := url.Parse(authorize)
	if err != nil || au.Query().Get("redirect_uri") == "" {
		return "", "", fmt.Errorf("not an authorize URL: %q", authorize)
	}
	redirect := au.Query().Get("redirect_uri")
	for range 20 {
		req, _ := http.NewRequest("GET", next, nil)
		resp, err := b.c.Do(req)
		if err != nil {
			return "", "", err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		switch {
		case resp.StatusCode/100 == 3 && (loc == redirect || strings.HasPrefix(loc, redirect+"?")):
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
		case "ak-stage-access-denied":
			return "", fmt.Errorf("the flow %s: %w", slug, errDenied)
		default:
			return "", fmt.Errorf("the flow %s answered %v: %v", slug, ch["component"], ch)
		}
	}
	return "", errors.New("the flow never ended")
}
