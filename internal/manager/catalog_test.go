package manager_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var rewrite = flag.Bool("rewrite-golden", false, "write the golden plans from what the manager makes")

// admin is a request to Admin's listener from a browser on this machine, as Admin's own page sends it.
func (p *primary) admin(method, path string, body any, edit ...func(*http.Request)) response {
	req := newRequest(method, path, body, thisMachine)
	req.Host = "127.0.0.1:7021"
	if method != http.MethodGet {
		req.Header.Set("Origin", "http://127.0.0.1:7021")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for _, e := range edit {
		e(req)
	}
	return serve(p.m.Admin(), req)
}

// setUp walks first-time setup's built steps, so the household has a name and an admin.
func (p *primary) setUp() {
	p.t.Helper()
	p.claimed().firstThree()
}

type catalogList struct {
	Catalog    struct{ ID, Name, Publisher string }
	Categories []struct{ ID, Name string }
	Apps       []struct {
		ID, Name, Purpose, Description, Category, Tier, Icon string
		SignIn                                               string `json:"signIn"`
	}
}

// Admin browses the official catalog: each app with its icon, screenshots, description and category. The CLI reads
// the same resource on the control socket.
func TestAdminBrowsesTheOfficialCatalog(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	r := p.admin("GET", "/api/v1/catalog", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if c := p.control("GET", "/api/v1/catalog", nil); !bytes.Equal(c.Body, r.Body) {
		t.Errorf("the control socket's catalog differs:\n%s\n%s", c.Body, r.Body)
	}
	var c catalogList
	r.json(t, &c)
	if c.Catalog.ID != "official" || len(c.Apps) != 2 || len(c.Categories) == 0 {
		t.Fatalf("catalog %+v", c)
	}
	signIn := map[string]string{}
	for _, a := range c.Apps {
		signIn[a.ID] = a.SignIn
		if a.Name == "" || a.Purpose == "" || a.Description == "" || a.Category == "" || a.Tier == "" {
			t.Errorf("%s: %+v", a.ID, a)
		}
		icon := p.admin("GET", a.Icon, nil)
		if icon.Code != http.StatusOK || icon.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(icon.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s's icon: %d %s, CSP %q", a.ID, icon.Code, icon.Header.Get("Content-Type"), icon.Header.Get("Content-Security-Policy"))
		}
		var page struct {
			Screenshots []struct{ URL, Caption string }
			App         struct{ ID string }
		}
		pr := p.admin("GET", "/api/v1/catalog/apps/"+a.ID, nil)
		pr.json(t, &page)
		if page.App.ID != a.ID || len(page.Screenshots) < 2 {
			t.Errorf("%s's page: %s", a.ID, pr.Body)
		}
		for _, s := range page.Screenshots {
			if img := p.admin("GET", s.URL, nil); img.Code != http.StatusOK || img.Header.Get("Content-Type") != "image/jpeg" || s.Caption == "" {
				t.Errorf("%s: %d %s", s.URL, img.Code, img.Header.Get("Content-Type"))
			}
		}
	}
	if signIn["immich"] != "oidc" || signIn["bentopdf"] != "guard" {
		t.Errorf("sign-in: %v, want Immich native (oidc) and BentoPDF guarded", signIn)
	}
	for _, path := range []string{"/api/v1/catalog/apps/nope", "/api/v1/catalog/apps/immich/icons", "/api/v1/catalog/apps/immich/screenshots/01", "/api/v1/catalog/apps/immich/screenshots/9", "/api/v1/catalog/apps/immich/screenshots/x"} {
		if r := p.admin("GET", path, nil); r.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, r.Code)
		}
	}
}

// The install planner turns a definition and the household's state into one plan, compared with a golden copy for
// both apps. Update them with go test ./internal/manager -run Golden -rewrite-golden.
func TestGoldenPlans(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.setUp()
	for _, app := range []string{"immich", "bentopdf"} {
		r := p.admin("POST", "/api/v1/plans", map[string]string{"app": app})
		if r.Code != http.StatusCreated {
			t.Fatalf("planning %s: %d %s", app, r.Code, r.Body)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, r.Body, "", "  "); err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "plans", app+".json")
		if *rewrite {
			if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(pretty.Bytes(), want) {
			t.Errorf("the plan for %s differs from %s:\n%s", app, golden, pretty.Bytes())
		}
	}
}

type madePlan struct {
	ID   string
	Name struct{ Host string }
}

// A plan is immutable and known by its id: the same household gets the same plan, a change in it gives a new plan,
// and the old one stays as it was. The database refuses to change a plan.
func TestAPlanIsImmutableAndKnownByItsID(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	b.firstThree()

	first := p.admin("POST", "/api/v1/plans", map[string]string{"app": "immich"})
	if first.Code != http.StatusCreated {
		t.Fatalf("%d %s", first.Code, first.Body)
	}
	var one madePlan
	first.json(t, &one)
	again := p.admin("POST", "/api/v1/plans", map[string]string{"app": "immich"})
	if again.Code != http.StatusOK || !bytes.Equal(again.Body, first.Body) {
		t.Errorf("planning again with nothing changed: %d %s, want 200 and the same plan", again.Code, again.Body)
	}
	for name, r := range map[string]response{"Admin": p.admin("GET", "/api/v1/plans/"+one.ID, nil), "control socket": p.control("GET", "/api/v1/plans/"+one.ID, nil)} {
		if r.Code != http.StatusOK || !bytes.Equal(r.Body, first.Body) {
			t.Errorf("%s: GET the plan by its id: %d %s", name, r.Code, r.Body)
		}
	}

	b.mustAnswer("you", map[string]string{"name": "Inês Ferreira", "mail": "ines@ferreira.casa"}) // a change in the household
	changed := p.admin("POST", "/api/v1/plans", map[string]string{"app": "immich"})
	var two madePlan
	changed.json(t, &two)
	if changed.Code != http.StatusCreated || two.ID == one.ID || two.Name.Host != "photos.ferreira.casa" {
		t.Errorf("after the household changed: %d, plan %s for %s, want a new plan for photos.ferreira.casa", changed.Code, two.ID, two.Name.Host)
	}
	if r := p.admin("GET", "/api/v1/plans/"+one.ID, nil); !bytes.Equal(r.Body, first.Body) {
		t.Errorf("the first plan changed: %s", r.Body)
	}

	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE plan SET body = '{}' WHERE id = ?`, one.ID); err == nil {
		t.Error("the database let a plan change")
	}
}

func TestNoPlanUntilTheHouseholdIsSetUp(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	if r := p.admin("POST", "/api/v1/plans", map[string]string{"app": "immich"}); r.Code != http.StatusConflict || r.errorCode(t) != "household-not-set-up" {
		t.Errorf("before setup: %d %s, want 409 household-not-set-up", r.Code, r.Body)
	}
	p.setUp()
	for name, c := range map[string]struct {
		body any
		code int
		err  string
	}{
		"an app the catalog hasn't": {map[string]string{"app": "nope"}, http.StatusNotFound, "no-such-app"},
		"no app":                    {map[string]string{}, http.StatusNotFound, "no-such-app"},
		"not JSON":                  {"[", http.StatusBadRequest, "not-json"},
		"another catalog's app":     {map[string]string{"catalog": "someone-else", "app": "immich"}, http.StatusNotFound, "no-such-catalog"},
	} {
		if r := p.admin("POST", "/api/v1/plans", c.body); r.Code != c.code || r.errorCode(t) != c.err {
			t.Errorf("%s: %d %s, want %d %s", name, r.Code, r.Body, c.code, c.err)
		}
	}
	if r := p.admin("GET", "/api/v1/plans/p-0123", nil); r.Code != http.StatusNotFound || r.errorCode(t) != "no-such-plan" {
		t.Errorf("an unknown plan: %d %s", r.Code, r.Body)
	}
}

// Until Admin has the household account's sign-in, its listener answers only this machine itself, by one of its
// loopback names at Admin's port, and takes changes only as JSON from Admin's own page.
func TestAdminAnswersOnlyThisMachine(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, withPages)
	plan := map[string]string{"app": "immich"}
	for name, c := range map[string]struct {
		method string
		body   any
		edit   func(*http.Request)
		code   int
		err    string
	}{
		"from the LAN":        {"GET", nil, func(r *http.Request) { r.RemoteAddr = onTheLAN + ":5000" }, http.StatusForbidden, "not-this-machine"},
		"from the mesh":       {"GET", nil, func(r *http.Request) { r.RemoteAddr = onTheMesh + ":5000" }, http.StatusForbidden, "not-this-machine"},
		"from a container":    {"GET", nil, func(r *http.Request) { r.RemoteAddr = aContainer + ":5000" }, http.StatusForbidden, "not-this-machine"},
		"a rebound name":      {"GET", nil, func(r *http.Request) { r.Host = "evil.example:7021" }, http.StatusMisdirectedRequest, "wrong-host"},
		"another port":        {"GET", nil, func(r *http.Request) { r.Host = "127.0.0.1:7020" }, http.StatusMisdirectedRequest, "wrong-host"},
		"another site's post": {"POST", plan, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, http.StatusForbidden, "cross-origin"},
		"a cross-site fetch":  {"POST", plan, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden, "cross-origin"},
		"a form, not JSON":    {"POST", plan, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, "not-json"},
		"setup's API":         {"GET", nil, func(r *http.Request) { r.URL.Path = "/api/v1/setup" }, http.StatusNotFound, "not-found"},
		"setup's links":       {"GET", nil, func(r *http.Request) { r.URL.Path = "/api/v1/setup/links" }, http.StatusNotFound, "not-found"},
	} {
		path := "/api/v1/catalog"
		if c.method == "POST" {
			path = "/api/v1/plans"
		}
		r := p.admin(c.method, path, c.body, c.edit)
		if r.Code != c.code || r.errorCode(t) != c.err {
			t.Errorf("%s: %d %s, want %d %s", name, r.Code, r.Body, c.code, c.err)
		}
	}
	for _, host := range []string{"localhost:7021", "[::1]:7021"} {
		if r := p.admin("GET", "/api/v1/catalog", nil, func(r *http.Request) { r.Host = host }); r.Code != http.StatusOK {
			t.Errorf("Host %s: %d", host, r.Code)
		}
	}
	r := p.admin("GET", "/store", nil)
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), "<title>admin</title>") {
		t.Fatalf("GET /store: %d %s", r.Code, r.Body)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if r.Header.Get(h) == "" {
			t.Errorf("Admin's page has no %s", h)
		}
	}
	for _, path := range []string{"/store/apps/immich", "/plans/p-0123"} {
		if r := p.admin("GET", path, nil); r.Code != http.StatusOK || !strings.Contains(string(r.Body), "<title>admin</title>") {
			t.Errorf("GET %s: %d", path, r.Code)
		}
	}
	if r := p.admin("GET", "/", nil); r.Code != http.StatusSeeOther || r.Header.Get("Location") != "/store" {
		t.Errorf("GET /: %d %s", r.Code, r.Header.Get("Location"))
	}
	if r := p.admin("GET", "/setup", nil); r.Code != http.StatusNotFound {
		t.Errorf("Admin serves setup's page: %d", r.Code)
	}
}

// The CLI finds Admin through the control socket.
func TestTheControlSocketSaysWhereAdminIs(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	var a struct{ URL string }
	r := p.control("GET", "/api/v1/admin", nil)
	r.json(t, &a)
	if r.Code != http.StatusOK || a.URL != "http://127.0.0.1:7021/store" {
		t.Errorf("%d %s", r.Code, r.Body)
	}
}
