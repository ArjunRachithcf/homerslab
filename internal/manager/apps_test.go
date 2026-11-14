package manager_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
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
func installable(t *testing.T, cat fs.FS) (*primary, *fakeSignIn) {
	t.Helper()
	p, _, fs := atJoin(t)
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

// A plan with an extra is a risky change, which needs the admin's passkey (#25): it is refused until step-up is built.
// Apps that sign people in themselves come with #20.
func TestOnlyGuardedPlansWithoutExtrasApplySoFar(t *testing.T) {
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
		t.Errorf("an OIDC app: %d %s", r.Code, r.Body)
	}
	if len(fs.guards) != 0 || len(agentApps(t, p).Apps) != 0 {
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
