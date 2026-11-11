package manager_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/catalogs"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var rewrite = flag.Bool("rewrite-golden", false, "write the golden plans from what the manager makes")

// admin is a request to Admin's socket, as the edge forwards it from Admin's own page at admin.<domain>, signed in as
// an admin (an Admin session, as a sign-in with the household account opens it: #17). Admin answers only once the
// domain is chosen (withDomain).
func (p *primary) admin(method, path string, body any, edit ...func(*http.Request)) response {
	req := newRequest(method, path, body, "@")
	req.Host = "admin.ferreira.family"
	req.AddCookie(&http.Cookie{Name: "__Host-homerslab-session", Value: p.adminSession()})
	if method != http.MethodGet {
		req.Header.Set("Origin", "https://admin.ferreira.family")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for _, e := range edit {
		e(req)
	}
	return serve(p.m.Admin(), req)
}

// adminSession is an Admin session's token, opened for ines as a sign-in would.
func (p *primary) adminSession() string {
	p.t.Helper()
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	const token = "admin-session-PLACEHOLDER"
	sum := sha256.Sum256([]byte(token))
	now := p.clock.Now()
	if _, err := db.Exec(`INSERT INTO admin_session (token_hash, username, created_at, expires_at) VALUES (?, 'ines', ?, ?)
		ON CONFLICT (token_hash) DO NOTHING`, sum[:], now.Add(-time.Minute).UnixMilli(), now.Add(time.Hour).UnixMilli()); err != nil {
		p.t.Fatal(err)
	}
	return token
}

// withDomain stands in for setup's domain step, ferreira.family, which setup reaches only after the sign-in step
// (#17): Admin answers at admin.<domain> once there is one.
func withDomain(p *primary) {
	p.t.Helper()
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO setup_answer (key, value) VALUES ('domain', 'ferreira.family')
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
		p.t.Fatal(err)
	}
}

// setUp walks first-time setup's built steps, so the household has a name and an admin, and stands in for its domain.
func (p *primary) setUp() {
	p.t.Helper()
	p.claimed().firstThree()
	withDomain(p)
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
	withDomain(p)
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
	ID       string
	Name     struct{ Host string }
	Audience struct{ People []struct{ Name string } }
}

// A plan is immutable and known by its id: the same household gets the same plan, a change in it gives a new plan,
// and the old one stays as it was. The database refuses to change a plan.
func TestAPlanIsImmutableAndKnownByItsID(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	b.firstThree()
	withDomain(p)

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

	b.mustAnswer("you", map[string]string{"name": "Inês Ferreira Rocha", "mail": "ines@ferreira.family"}) // a change in the household
	changed := p.admin("POST", "/api/v1/plans", map[string]string{"app": "immich"})
	var two madePlan
	changed.json(t, &two)
	if changed.Code != http.StatusCreated || two.ID == one.ID || len(two.Audience.People) != 1 || two.Audience.People[0].Name != "Inês Ferreira Rocha" {
		t.Errorf("after the household changed: %d, plan %s for %+v, want a new plan", changed.Code, two.ID, two.Audience)
	}
	if r := p.admin("GET", "/api/v1/plans/"+one.ID, nil); !bytes.Equal(r.Body, first.Body) {
		t.Errorf("the first plan changed: %s", r.Body)
	}

	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, q := range map[string]string{
		"an update":            `UPDATE plan SET body = '{}' WHERE id = ?`,
		"a delete":             `DELETE FROM plan WHERE id = ?`,
		"an insert or replace": `INSERT OR REPLACE INTO plan (id, app, body, made_at) VALUES (?, 'immich', '{}', 0)`,
		"a replace":            `REPLACE INTO plan (id, app, body, made_at) VALUES (?, 'immich', '{}', 0)`,
	} {
		if _, err := db.Exec(q, one.ID); err == nil {
			t.Errorf("the database let %s change a plan", name)
		}
	}
	if r := p.admin("GET", "/api/v1/plans/"+one.ID, nil); !bytes.Equal(r.Body, first.Body) {
		t.Errorf("the first plan changed: %s", r.Body)
	}
}

func TestNoPlanUntilTheHouseholdIsSetUp(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	withDomain(p)
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

// Admin takes a change only as JSON from its own page, served over HTTPS at its name; it serves its pages at their
// addresses, and nothing of setup's.
func TestAdminTakesChangesOnlyFromItsOwnPage(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, withPages)
	p.setUp()
	plan := map[string]string{"app": "immich"}
	for name, c := range map[string]struct {
		edit func(*http.Request)
		code int
		err  string
	}{
		"another site's post":    {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden, "cross-origin"},
		"Admin's name over HTTP": {func(r *http.Request) { r.Header.Set("Origin", "http://admin.ferreira.family") }, http.StatusForbidden, "cross-origin"},
		"no Origin at all":       {func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden, "cross-origin"},
		"a cross-site fetch":     {func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden, "cross-origin"},
		"a form, not JSON":       {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, "not-json"},
		"another name":           {func(r *http.Request) { r.Host = "photos.ferreira.family" }, http.StatusMisdirectedRequest, "wrong-host"},
	} {
		if r := p.admin("POST", "/api/v1/plans", plan, c.edit); r.Code != c.code || r.errorCode(t) != c.err {
			t.Errorf("%s: %d %s, want %d %s", name, r.Code, r.Body, c.code, c.err)
		}
	}
	if r := p.admin("POST", "/api/v1/plans", plan, func(r *http.Request) { r.Header.Set("Origin", "https://admin.ferreira.family:9443") }); r.Code/100 != 2 {
		t.Errorf("from Admin's page on the public port: %d %s", r.Code, r.Body)
	}
	for _, path := range []string{"/", "/store", "/store/apps/immich", "/plans/p-0123"} {
		r := p.admin("GET", path, nil)
		if r.Code != http.StatusOK || !strings.Contains(string(r.Body), "<title>admin</title>") {
			t.Errorf("GET %s: %d %s", path, r.Code, r.Body)
		}
		for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
			if r.Header.Get(h) == "" {
				t.Errorf("GET %s has no %s", path, h)
			}
		}
	}
	for _, path := range []string{"/setup", "/api/v1/setup", "/api/v1/setup/links", "/api/v1/nodes/primary"} {
		if r := p.admin("GET", path, nil); r.Code != http.StatusNotFound {
			t.Errorf("Admin serves %s: %d", path, r.Code)
		}
	}
}

// A plan's id covers the definition it was made from: the same app with another compose file is another plan.
func TestAPlanCoversItsDefinition(t *testing.T) {
	testenv.Isolate(t)
	plans := map[string]string{}
	for name, compose := range map[string]func([]byte) []byte{
		"as shipped": func(b []byte) []byte { return b },
		"another time zone": func(b []byte) []byte {
			return bytes.Replace(b, []byte("    security_opt:"), []byte("    environment: {TZ: UTC}\n    security_opt:"), 1)
		},
	} {
		cat := fstest.MapFS{}
		err := fs.WalkDir(catalogs.Official(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(catalogs.Official(), p)
			if p == "apps/bentopdf/compose.yaml" {
				b = compose(b)
			}
			cat[p] = &fstest.MapFile{Data: b}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		p := newPrimary(t, func(p *primary) { p.catalog = cat })
		p.setUp()
		var made madePlan
		r := p.admin("POST", "/api/v1/plans", map[string]string{"app": "bentopdf"})
		r.json(t, &made)
		if r.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body)
		}
		plans[name] = made.ID
	}
	if plans["as shipped"] == plans["another time zone"] {
		t.Errorf("a changed compose file kept plan %s", plans["as shipped"])
	}
}

// Admin's Store and plans want an Admin session, as the rest of Admin does: signed out, a page goes to the household
// account's sign-in and an API call is refused (#17).
func TestTheStoreWantsAnAdminSession(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, withPages)
	p.setUp()
	signedOut := func(method, path string, body any) response {
		req := newRequest(method, path, body, "@")
		req.Host = "admin.ferreira.family"
		req.Header.Set("Origin", "https://admin.ferreira.family")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		return serve(p.m.Admin(), req)
	}
	for _, path := range []string{"/", "/store", "/store/apps/immich", "/plans/p-0123"} {
		if r := signedOut("GET", path, nil); r.Code != http.StatusSeeOther || r.Header.Get("Location") != "/auth/sign-in" {
			t.Errorf("GET %s signed out: %d %s", path, r.Code, r.Header.Get("Location"))
		}
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{{"GET", "/api/v1/catalog", nil}, {"GET", "/api/v1/catalog/apps/immich", nil}, {"GET", "/api/v1/catalog/apps/immich/icon", nil},
		{"POST", "/api/v1/plans", map[string]string{"app": "immich"}}, {"GET", "/api/v1/plans/p-0123", nil}} {
		if r := signedOut(c.method, c.path, c.body); r.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out: %d %s", c.method, c.path, r.Code, r.Body)
		}
	}
	if r := p.control("GET", "/api/v1/catalog", nil); r.Code != http.StatusOK {
		t.Errorf("the control socket's catalog: %d", r.Code)
	}
}
