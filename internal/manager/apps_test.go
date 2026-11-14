package manager_test

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/catalogs"
	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// installable is a primary whose household account is set up (setup's second half has begun: `setup join`), where an
// admin can apply an install plan; cat is its catalog, the official one when nil.
func installable(t *testing.T, cat fs.FS, opts ...option) (*primary, *fakeSignIn) {
	t.Helper()
	p, _, fs := atJoin(t, opts...)
	if cat != nil {
		p.catalog = cat
		p.open()
	}
	if r := p.control("POST", "/api/v1/setup/join", nil); r.Code != http.StatusOK {
		t.Fatalf("setup join: %d %s", r.Code, r.Body)
	}
	return p, fs
}

// planFor makes an app's install plan, as the CLI does on the control socket.
func planFor(t *testing.T, p *primary, app string) madePlan {
	t.Helper()
	r := p.control("POST", "/api/v1/plans", map[string]string{"app": app})
	if r.Code != http.StatusCreated && r.Code != http.StatusOK {
		t.Fatalf("planning %s: %d %s", app, r.Code, r.Body)
	}
	var made madePlan
	r.json(t, &made)
	return made
}

// agentApps is every app the agent runs, as it says.
func agentApps(t *testing.T, p *primary) agent.AppsStatus {
	t.Helper()
	var s agent.AppsStatus
	serveAgent(t, p.agent, "GET", "/v1/apps").json(t, &s)
	return s
}

// withCatalog is the official catalog with its files changed by edit (the app's folder relative path → its new body,
// nil to leave it as it is).
func withCatalog(t *testing.T, edit map[string]func([]byte) []byte) fs.FS {
	t.Helper()
	cat := fstest.MapFS{}
	err := fs.WalkDir(catalogs.Official(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(catalogs.Official(), path)
		if f := edit[path]; f != nil {
			b = f(b)
		}
		cat[path] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// Applying takes the plan's id and does exactly what the plan said, nothing more (#19): the app's guard for its name and
// audience, and the app on its node, its name, upstream and checks as the plan and its definition say. The household
// then has the app, and its health from the agent.
func TestApplyingAPlanDoesWhatItSaysAndNothingMore(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	made := planFor(t, p, "bentopdf")
	if len(fs.guards) != 0 || len(agentApps(t, p).Apps) != 0 {
		t.Fatal("a plan changed something before it was applied")
	}
	r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil)
	if r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	want := []idp.Guard{{Slug: "homerslab-bentopdf", Name: "BentoPDF", URL: "https://pdf.example.test:9443", Roles: []idp.Role{idp.Admin, idp.Adult}}}
	if !reflect.DeepEqual(fs.guards, want) {
		t.Errorf("the guards: %+v, want %+v", fs.guards, want)
	}
	s := agentApps(t, p)
	if len(s.Apps) != 1 {
		t.Fatalf("the agent runs %+v", s.Apps)
	}
	a := s.Apps[0]
	if a.ID != "bentopdf" || a.Plan != made.ID || a.Name != "pdf.example.test" || a.Upstream != "172.31.240.2:8080" || a.Product != "BentoPDF" {
		t.Errorf("the agent's app: %+v", a)
	}
	// The household's state: the app, the plan applied to it, and its health from the agent.
	var got struct {
		Apps []struct {
			ID, Name, Host, URL, Plan, Health string
			Active                            bool
		}
		Drift []agent.AppDrift
	}
	p.control("GET", "/api/v1/apps", nil).json(t, &got)
	if len(got.Apps) != 1 || got.Apps[0].ID != "bentopdf" || got.Apps[0].Plan != made.ID || got.Apps[0].URL != "https://pdf.example.test:9443" ||
		got.Apps[0].Health != "healthy" || !got.Apps[0].Active || len(got.Drift) != 0 {
		t.Errorf("the household's apps: %+v", got)
	}
	// Applied again, the same plan does the same: one app, one guard brought in line.
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted || len(agentApps(t, p).Apps) != 1 {
		t.Errorf("applied again: %d %s", r.Code, r.Body)
	}
}

// A plan applies only while it is still the plan the household would get: a changed definition or household makes a
// new plan, and the old one is refused rather than applied (it would do something its page didn't say).
func TestAStalePlanIsRefused(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	made := planFor(t, p, "bentopdf")

	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE setup_answer SET value = 'Inês Ferreira-Costa' WHERE key = 'name'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusConflict || !strings.Contains(string(r.Body), "stale-plan") {
		t.Errorf("after the household changed: %d %s", r.Code, r.Body)
	}
	fresh := planFor(t, p, "bentopdf")
	p.catalog = withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/compose.yaml": func(b []byte) []byte {
		return bytes.Replace(b, []byte("    security_opt:"), []byte("    environment: {TZ: UTC}\n    security_opt:"), 1)
	}})
	p.open() // the catalog moved on
	if r := p.control("POST", "/api/v1/plans/"+fresh.ID+"/apply", nil); r.Code != http.StatusConflict || !strings.Contains(string(r.Body), "stale-plan") {
		t.Errorf("after the definition changed: %d %s", r.Code, r.Body)
	}
	if r := p.control("POST", "/api/v1/plans/p-00000000000000000000000000000000/apply", nil); r.Code != http.StatusNotFound {
		t.Errorf("a plan that was never made: %d %s", r.Code, r.Body)
	}
	if len(fs.guards) != 0 || len(agentApps(t, p).Apps) != 0 {
		t.Error("a refused plan changed something")
	}
}

// An app that signs people in itself (#20): applying its plan makes its OIDC client in the household account, for the
// plan's audience and exactly the redirect addresses the plan names (the app's callbacks on its one URL, the household's
// port included), and hands the agent the client's settings and secret. The secret reaches the agent inward only: the
// manager's database and logs never hold it, and the agent keeps it only sealed. No guard is made for it.
func TestApplyingANativePlanMakesItsClientAndHandsTheAppItsSecret(t *testing.T) {
	testenv.Isolate(t)
	var logs lockedBuffer
	p, fs := installable(t, nil, withLogs(&logs))
	made := planFor(t, p, "mealie")
	var shown struct {
		SignIn struct {
			Method, Creates string
			RedirectURIs    []string
		}
	}
	p.control("GET", "/api/v1/plans/"+made.ID, nil).json(t, &shown)
	if shown.SignIn.Method != "oidc" || shown.SignIn.Creates != "oidc-client" ||
		!reflect.DeepEqual(shown.SignIn.RedirectURIs, []string{"https://recipes.example.test:9443/login"}) {
		t.Errorf("the plan's sign-in: %+v", shown.SignIn)
	}
	if len(appClients(fs)) != 0 {
		t.Fatal("a plan made a client before it was applied")
	}
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	want := []idp.Client{{Slug: "homerslab-mealie", Name: "Mealie", RedirectURIs: []string{"https://recipes.example.test:9443/login"}, Roles: []idp.Role{idp.Admin, idp.Adult}}}
	if !reflect.DeepEqual(appClients(fs), want) || len(fs.guards) != 0 {
		t.Errorf("the clients: %+v, want %+v; guards %+v, want none", appClients(fs), want, fs.guards)
	}
	s := agentApps(t, p)
	if len(s.Apps) != 1 || s.Apps[0].ID != "mealie" || s.Apps[0].Name != "recipes.example.test" {
		t.Fatalf("the agent runs %+v", s.Apps)
	}
	agentDir := filepath.Dir(p.host.Root)
	override, err := os.ReadFile(filepath.Join(agentDir, "state", "apps", "mealie", "homerslab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := strings.Join(strings.Fields(string(p.host.Edge.Config())), ""); !strings.Contains(cfg, `"path":["/api/auth/token","/api/auth/token/*"]`) {
		t.Errorf("the edge doesn't refuse Mealie's own password sign-in:\n%s", cfg)
	}
	for _, want := range []string{
		`"OIDC_CONFIGURATION_URL": "https://auth.example.test:9443/application/o/homerslab-mealie/.well-known/openid-configuration"`,
		`"OIDC_CLIENT_ID": "homerslab-mealie"`, `"OIDC_CLIENT_SECRET_FILE": "/run/secrets/oidc-client-secret"`,
		`"BASE_URL": "https://recipes.example.test:9443"`, `"OIDC_ADMIN_GROUP": "homerslab-admins"`,
	} {
		if !strings.Contains(string(override), want) {
			t.Errorf("the app's settings lack %s:\n%s", want, override)
		}
	}
	secret := "CLIENT-SECRET-PLACEHOLDER-homerslab-mealie"
	cred, err := os.ReadFile(filepath.Join(agentDir, "state", "apps", "mealie", "credentials", "oidc-client-secret.cred"))
	if err != nil || string(cred) != "sealed:"+base64.StdEncoding.EncodeToString([]byte(secret)) {
		t.Errorf("the client's secret, sealed by the agent: %q %v", cred, err)
	}
	db, _ := os.ReadFile(p.db)
	wal, _ := os.ReadFile(p.db + "-wal")
	for where, b := range map[string]string{"the database": string(db) + string(wal), "the logs": logs.String(), "the answers": fmt.Sprint(s)} {
		if strings.Contains(b, secret) {
			t.Errorf("%s hold the client's secret", where)
		}
	}
}

// A native app whose install fails after its client was made leaves no client behind (#20): the household account
// holds no client for an app that isn't there.
func TestANativeInstallThatFailsDeletesItsNewClient(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	made := planFor(t, p, "mealie")
	p.host.Fail("systemctl enable")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code/100 == 2 {
		t.Fatalf("applying with systemd failing: %d %s", r.Code, r.Body)
	}
	if len(appClients(fs)) != 1 || !reflect.DeepEqual(fs.deleted, []string{"homerslab-mealie"}) {
		t.Errorf("clients made %+v, deleted %v: want the one made, deleted", appClients(fs), fs.deleted)
	}
	p.host.Mend()
	fs.clientFails = errors.New("the household account is down")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusBadGateway || !strings.Contains(string(r.Body), "client-failed") {
		t.Errorf("with the household account failing: %d %s", r.Code, r.Body)
	}
	if len(agentApps(t, p).Apps) != 0 {
		t.Error("an app was installed without its client")
	}
}

// A plan with an extra is a risky change, which needs the admin's passkey (#25): it is refused until step-up is built.
// An app with native sign-in that can't take its client from HomersLab yet (Immich: no settings in its manifest), and
// LDAP apps (#49), aren't applied either.
func TestOnlyPlansThatCanBeWiredWithoutExtrasApplySoFar(t *testing.T) {
	testenv.Isolate(t)
	cat := withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		return bytes.Replace(b, []byte("extras: []"), []byte("extras:\n  - {id: printer, kind: device, service: web, device: /dev/usb/lp0, why: To print}"), 1)
	}})
	p, fs := installable(t, cat)
	risky := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+risky.ID+"/apply", nil); r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), "step-up-required") {
		t.Errorf("a plan with an extra: %d %s", r.Code, r.Body)
	}
	oidc := planFor(t, p, "immich")
	if r := p.control("POST", "/api/v1/plans/"+oidc.ID+"/apply", nil); r.Code != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "sign-in-not-built") {
		t.Errorf("an OIDC app that can't take its client yet: %d %s", r.Code, r.Body)
	}
	if len(fs.guards) != 0 || len(appClients(fs)) != 0 || len(agentApps(t, p).Apps) != 0 {
		t.Error("a refused plan changed something")
	}
}

// Compose project names belong to HomersLab: a project of the app's name that the engine already runs, which HomersLab
// didn't create, refuses the install.
func TestAPlanIsNotAppliedOverAProjectHomersLabDidntCreate(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	p.host.Project(engine.State{Name: "homerslab-bentopdf", Containers: []engine.Container{{Service: "web", Name: "homerslab-bentopdf-web-1", State: "running"}}})
	made := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "not-ours") {
		t.Errorf("over someone else's project: %d %s", r.Code, r.Body)
	}
	// Refused before anything is made: no guard in the identity provider either.
	if len(fs.guards) != 0 {
		t.Errorf("a refused plan made a guard: %+v", fs.guards)
	}
	var got struct{ Apps []any }
	p.control("GET", "/api/v1/apps", nil).json(t, &got)
	if len(got.Apps) != 0 {
		t.Errorf("the household has %v", got.Apps)
	}
}

// Admin applies a plan from its own page, signed in as an admin; signed out, or from another site, it is refused.
func TestAdminAppliesAPlanFromItsOwnPage(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	made := planFor(t, p, "bentopdf")
	a := p.adminBrowser(t)
	body := func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")); r.ContentLength = 2 }
	if rec := a.do("POST", "/api/v1/plans/"+made.ID+"/apply", sameOrigin, body); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d %s", rec.Code, rec.Body)
	}
	fs.passkeys["ines"] = 1
	if rec := a.signIn(fs, "ines", "homerslab-admins"); rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in: %d", rec.Code)
	}
	crossSite := func(r *http.Request) {
		sameOrigin(r)
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.Header.Set("Origin", "https://pdf.example.test:9443")
	}
	if rec := a.do("POST", "/api/v1/plans/"+made.ID+"/apply", crossSite, body); rec.Code != http.StatusForbidden {
		t.Errorf("from an app's page: %d %s", rec.Code, rec.Body)
	}
	if len(fs.guards) != 0 {
		t.Fatal("a refused apply made a guard")
	}
	rec := a.do("POST", "/api/v1/plans/"+made.ID+"/apply", sameOrigin, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("from Admin's page: %d %s", rec.Code, rec.Body)
	}
	var v struct{ ID, Health string }
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.ID != "bentopdf" {
		t.Errorf("applied: %s", rec.Body)
	}
	if rec := a.do("GET", "/api/v1/apps"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"health":"healthy"`) {
		t.Errorf("the apps in Admin: %d %s", rec.Code, rec.Body)
	}
}

// appClients are the apps' OIDC clients the manager made, without Admin's and Home's own.
func appClients(fs *fakeSignIn) []idp.Client {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var out []idp.Client
	for _, c := range fs.clients {
		if c.Slug != "homerslab-admin" && c.Slug != "homerslab-home" {
			out = append(out, c)
		}
	}
	return out
}
