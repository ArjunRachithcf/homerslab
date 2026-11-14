package home_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// world is Home with its projection, the sign-in's stand-in, and the built pages.
type world struct {
	t          *testing.T
	h          http.Handler
	is         *issuer
	projection string
	now        time.Time
	// restart is Home's process starting again: a new Home, on the same projection and secret.
	restart func()
}

var pages = fstest.MapFS{
	"home.html":                 {Data: []byte("<!doctype html><title>Home</title>")},
	"admin.html":                {Data: []byte("<!doctype html><title>Admin</title>")},
	"assets/home-abc.js":        {Data: []byte("console.log('home')")},
	"home/manifest.webmanifest": {Data: []byte(`{"name":"Home"}`)},
	"home/icon-192.png":         {Data: []byte("\x89PNG-192")},
}

// theProjection is the Ferreira household's: three apps, for three audiences, in three states.
func theProjection(is *issuer) home.Projection {
	return home.Projection{
		Household: "The Ferreira household", Home: homeOrigin, Admin: "https://admin.example.test:9443",
		SignIn: &home.SignIn{Issuer: issuerURL, ClientID: clientID, Upstream: is.srv.URL},
		Apps: []home.App{
			{ID: "bentopdf", Name: "BentoPDF", Purpose: "PDF tools", Category: "documents", URL: "https://pdf.example.test:9443",
				Audience: []string{"admin", "adult"}, Health: "healthy", Icon: &home.Icon{Type: "image/svg+xml", Data: []byte("<svg/>")}},
			{ID: "donetick", Name: "Donetick", Purpose: "Chores", Category: "home", URL: "https://chores.example.test:9443",
				Audience: []string{"admin", "adult", "kid"}, Health: "unhealthy"},
			{ID: "termix", Name: "Termix", Purpose: "Terminal", Category: "tools", URL: "https://term.example.test:9443",
				Audience: []string{"admin"}, Health: "starting"},
		},
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	w.is = newIssuer(t, func() time.Time { return w.now })
	w.projection = filepath.Join(t.TempDir(), "home.json")
	w.write(theProjection(w.is))
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(clientSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	w.restart = func() {
		w.h = home.New(home.Config{Projection: w.projection, ClientSecret: secret, Web: pages, Now: func() time.Time { return w.now }}).Handler()
	}
	w.restart()
	return w
}

func (w *world) write(p home.Projection) {
	w.t.Helper()
	if err := home.Write(w.projection, p); err != nil {
		w.t.Fatal(err)
	}
}

// browser is a browser at Home's name through the edge, keeping Home's cookies as a browser would.
type browser struct {
	w       *world
	cookies map[string]*http.Cookie
}

func (w *world) browser() *browser { return &browser{w: w, cookies: map[string]*http.Cookie{}} }

func (b *browser) do(method, path string, edit ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, homeOrigin+path, strings.NewReader("{}"))
	req.RemoteAddr = "@" // the edge, on Home's socket
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	for _, e := range edit {
		e(req)
	}
	rec := httptest.NewRecorder()
	b.w.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly {
			b.w.t.Errorf("Home set the cookie %s: want __Host-, Secure, HttpOnly, Path=/ and no Domain (#83)", c)
		}
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return rec
}

// signIn goes from Home to the sign-in and back, as p.
func (b *browser) signIn(p person) *httptest.ResponseRecorder {
	b.w.t.Helper()
	rec := b.do("GET", "/auth/sign-in")
	if rec.Code != http.StatusSeeOther {
		b.w.t.Fatalf("GET /auth/sign-in: %d %s", rec.Code, rec.Body)
	}
	state, code := b.w.is.code(b.w.t, rec.Header().Get("Location"), p)
	return b.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code))
}

// sameOrigin is a change from Home's own page: JSON, from Home's origin.
func sameOrigin(r *http.Request) {
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", homeOrigin)
	r.Header.Set("Content-Type", "application/json")
}

type homeView struct {
	Household string
	Person    struct{ Username, Name, Role string }
	Admin     string
	Apps      []struct{ ID, Name, Purpose, Category, URL, Health, Icon string }
}

func (b *browser) home() homeView {
	b.w.t.Helper()
	rec := b.do("GET", "/api/v1/home")
	if rec.Code != http.StatusOK {
		b.w.t.Fatalf("GET /api/v1/home: %d %s", rec.Code, rec.Body)
	}
	var v homeView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		b.w.t.Fatal(err)
	}
	return v
}

// Home signs people in with the household account (#21): an OIDC client of its own at the sign-in, the code flow with
// PKCE; a session of its own afterwards, in a __Host- cookie. Signed out, its page goes to the sign-in and its API says
// so.
func TestHomeSignsPeopleInWithTheHouseholdAccount(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	b := w.browser()
	if r := b.do("GET", "/"); r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/auth/sign-in" {
		t.Errorf("signed out, GET / = %d %s", r.Code, r.Header().Get("Location"))
	}
	if r := b.do("GET", "/api/v1/home"); r.Code != http.StatusUnauthorized {
		t.Errorf("signed out, GET /api/v1/home = %d", r.Code)
	}
	r := b.signIn(rui)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/" {
		t.Fatalf("the sign-in's return: %d %s %s", r.Code, r.Header().Get("Location"), r.Body)
	}
	if _, ok := b.cookies["__Host-homerslab-home"]; !ok {
		t.Errorf("no session cookie: %v", b.cookies)
	}
	if r := b.do("GET", "/"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "<title>Home</title>") {
		t.Errorf("signed in, GET / = %d %s", r.Code, r.Body)
	}
	if v := b.home(); v.Person.Username != "rui" || v.Person.Name != "Rui Ferreira" || v.Person.Role != "adult" || v.Household != "The Ferreira household" {
		t.Errorf("who Home says is looking: %+v", v)
	}

	// Signing out ends the session, from Home's own page.
	if r := b.do("POST", "/auth/sign-out", sameOrigin); r.Code != http.StatusNoContent {
		t.Errorf("signing out: %d %s", r.Code, r.Body)
	}
	if r := b.do("GET", "/api/v1/home"); r.Code != http.StatusUnauthorized {
		t.Errorf("after signing out: %d", r.Code)
	}
}

// Only the household's people get in: someone in none of its roles is refused, and so is a return that isn't this
// browser's sign-in.
func TestHomeLetsInOnlyTheHouseholdsPeople(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	if r := w.browser().signIn(guest); r.Code != http.StatusForbidden || !strings.Contains(r.Body.String(), "not-in-the-household") {
		t.Errorf("someone in no role: %d %s", r.Code, r.Body)
	}
	b := w.browser()
	rec := b.do("GET", "/auth/sign-in")
	state, code := w.is.code(t, rec.Header().Get("Location"), rui)
	other := w.browser() // another browser, signing in itself, with the first one's return
	other.do("GET", "/auth/sign-in")
	if r := other.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code)); r.Code != http.StatusForbidden {
		t.Errorf("another browser's return: %d %s", r.Code, r.Body)
	}
	if r := b.do("GET", "/auth/callback?state=forged&code="+url.QueryEscape(code)); r.Code != http.StatusForbidden {
		t.Errorf("a forged state: %d %s", r.Code, r.Body)
	}
	if r := b.do("GET", "/api/v1/home", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__Host-homerslab-home", Value: "made-up"}) }); r.Code != http.StatusUnauthorized {
		t.Errorf("a made-up session: %d", r.Code)
	}
	// A session ends with its life.
	b = w.browser()
	b.signIn(rui)
	w.now = w.now.Add(home.SessionLife + time.Minute)
	if r := b.do("GET", "/api/v1/home"); r.Code != http.StatusUnauthorized {
		t.Errorf("a session past its life: %d", r.Code)
	}
}

// One tile per app the person may open (#21, story 29): the apps whose audience has their role, each with its health
// and its one URL, and nothing of the others. An admin also gets Admin's address.
func TestHomeShowsEachPersonTheAppsTheirAudienceMayOpen(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	for _, c := range []struct {
		p     person
		role  string
		apps  []string
		admin bool
	}{
		{ines, "admin", []string{"bentopdf:healthy:https://pdf.example.test:9443", "donetick:unhealthy:https://chores.example.test:9443", "termix:starting:https://term.example.test:9443"}, true},
		{rui, "adult", []string{"bentopdf:healthy:https://pdf.example.test:9443", "donetick:unhealthy:https://chores.example.test:9443"}, false},
		{tomas, "kid", []string{"donetick:unhealthy:https://chores.example.test:9443"}, false},
	} {
		t.Run(c.p.username, func(t *testing.T) {
			b := w.browser()
			b.signIn(c.p)
			v := b.home()
			var got []string
			for _, a := range v.Apps {
				got = append(got, a.ID+":"+a.Health+":"+a.URL)
			}
			if !slices.Equal(got, c.apps) || v.Person.Role != c.role {
				t.Errorf("%s (%s) sees %v, want %v", c.p.username, v.Person.Role, got, c.apps)
			}
			if (v.Admin != "") != c.admin || (c.admin && v.Admin != "https://admin.example.test:9443") {
				t.Errorf("%s gets Admin's address %q", c.p.username, v.Admin)
			}
			// The icons too: only of the apps they may open.
			for _, id := range []string{"bentopdf", "donetick", "termix"} {
				r := b.do("GET", "/api/v1/apps/"+id+"/icon")
				may := slices.ContainsFunc(c.apps, func(s string) bool { return strings.HasPrefix(s, id+":") })
				switch {
				case !may && r.Code != http.StatusNotFound:
					t.Errorf("%s reads %s's icon: %d", c.p.username, id, r.Code)
				case may && id == "bentopdf" && (r.Code != http.StatusOK || r.Body.String() != "<svg/>" || r.Header().Get("Content-Type") != "image/svg+xml" ||
					!strings.Contains(r.Header().Get("Content-Security-Policy"), "sandbox")):
					t.Errorf("%s reads %s's icon: %d %q %v", c.p.username, id, r.Code, r.Body, r.Header())
				}
			}
		})
	}

	// What the projection says now is what Home shows: an app installed since, a health that changed.
	b := w.browser()
	b.signIn(rui)
	p := theProjection(w.is)
	p.Apps[0].Health = "unhealthy"
	p.Apps = append(p.Apps, home.App{ID: "immich", Name: "Immich", URL: "https://photos.example.test:9443", Audience: []string{"admin", "adult"}, Health: "healthy"})
	w.write(p)
	var got []string
	for _, a := range b.home().Apps {
		got = append(got, a.ID+":"+a.Health)
	}
	if want := []string{"bentopdf:unhealthy", "donetick:unhealthy", "immich:healthy"}; !slices.Equal(got, want) {
		t.Errorf("after the projection changed: %v, want %v", got, want)
	}
}

// Every app is same-site with Home, so Home refuses cross-site changes (#83): a change needs Sec-Fetch-Site:
// same-origin and Home's exact Origin, as JSON. Its session cookie is __Host- prefixed (browser.do checks every
// cookie Home sets).
func TestHomeRefusesCrossSiteChanges(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	b := w.browser()
	b.signIn(rui)
	set := func(site, origin, typ string) func(*http.Request) {
		return func(r *http.Request) {
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			r.Header.Set("Content-Type", typ)
		}
	}
	for name, edit := range map[string]func(*http.Request){
		"from an app's page (same-site)":                 set("same-site", "https://pdf.example.test:9443", "application/json"),
		"from an app's page, claiming same-origin":       set("same-origin", "https://pdf.example.test:9443", "application/json"),
		"from another site":                              set("cross-site", "https://evil.example", "application/json"),
		"Home's origin, but same-site":                   set("same-site", homeOrigin, "application/json"),
		"no Sec-Fetch-Site":                              set("", homeOrigin, "application/json"),
		"no Origin":                                      set("same-origin", "", "application/json"),
		"Home's name at another port":                    set("same-origin", "https://home.example.test", "application/json"),
		"Home's name over plain HTTP":                    set("same-origin", "http://home.example.test:9443", "application/json"),
		"Home's name with a path":                        set("same-origin", homeOrigin+"/", "application/json"),
		"Origin null":                                    set("same-origin", "null", "application/json"),
		"a form post from Home's origin":                 set("same-origin", homeOrigin, "application/x-www-form-urlencoded"),
		"Sec-Fetch-Site none (typed in the address bar)": set("none", homeOrigin, "application/json"),
	} {
		t.Run(name, func(t *testing.T) {
			r := b.do("POST", "/auth/sign-out", edit)
			if r.Code != http.StatusForbidden && r.Code != http.StatusUnsupportedMediaType {
				t.Errorf("POST /auth/sign-out: %d %s", r.Code, r.Body)
			}
			if b.do("GET", "/api/v1/home").Code != http.StatusOK {
				t.Fatal("the refused change signed the person out")
			}
		})
	}
	if r := b.do("POST", "/auth/sign-out", sameOrigin); r.Code != http.StatusNoContent {
		t.Errorf("from Home's own page: %d %s", r.Code, r.Body)
	}
}

// Home answers only its own name, whatever the edge forwards; and it says when the manager hasn't made its projection
// yet, rather than showing anything.
func TestHomeAnswersOnlyItsOwnName(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	for _, host := range []string{"admin.example.test:9443", "pdf.example.test", "home.other.test:9443"} {
		r := w.browser().do("GET", "/auth/sign-in", func(r *http.Request) { r.Host = host })
		if r.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: %d", host, r.Code)
		}
	}
	if r := w.browser().do("GET", "/auth/sign-in", func(r *http.Request) { r.Host = "HOME.example.test:9443" }); r.Code != http.StatusSeeOther {
		t.Errorf("Home's name in capitals: %d", r.Code)
	}
	if err := os.Remove(w.projection); err != nil {
		t.Fatal(err)
	}
	if r := w.browser().do("GET", "/auth/sign-in"); r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "not-ready") {
		t.Errorf("without a projection: %d %s", r.Code, r.Body)
	}
}

// Home installs on a phone as a PWA (#21, story 30): its manifest and the logo's icons are there without a session,
// as a browser fetches a manifest without cookies; the page itself isn't. Every answer carries the surfaces' security
// headers.
func TestHomesManifestAndIconsNeedNoSession(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	b := w.browser()
	for path, want := range map[string]string{
		"/manifest.webmanifest": "application/manifest+json",
		"/home/icon-192.png":    "image/png",
		"/assets/home-abc.js":   "text/javascript; charset=utf-8",
	} {
		r := b.do("GET", path)
		if r.Code != http.StatusOK || r.Header().Get("Content-Type") != want {
			t.Errorf("GET %s: %d %s", path, r.Code, r.Header().Get("Content-Type"))
		}
	}
	for _, path := range []string{"/admin.html", "/home.html", "/home/../admin.html", "/assets/../admin.html", "/home/missing.png"} {
		if r := b.do("GET", path); r.Code == http.StatusOK {
			t.Errorf("GET %s: %d %s", path, r.Code, r.Body)
		}
	}
	r := b.do("GET", "/manifest.webmanifest")
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if r.Header().Get(k) != v {
			t.Errorf("%s = %q, want %q", k, r.Header().Get(k), v)
		}
	}
	if csp := r.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
}

// Signing out of Home ends the household account's say on that browser too: the next sign-in there is a fresh one (a
// shared tablet handed to someone else never opens on the last person's Home), until it succeeds.
func TestAfterSigningOutTheNextSignInIsAFreshOne(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	b := w.browser()
	if loc := b.do("GET", "/auth/sign-in").Header().Get("Location"); strings.Contains(loc, "max_age=") {
		t.Errorf("a first sign-in asks for a fresh one: %s", loc)
	}
	b = w.browser()
	b.signIn(rui)
	b.do("POST", "/auth/sign-out", sameOrigin)
	if loc := b.do("GET", "/auth/sign-in").Header().Get("Location"); !strings.Contains(loc, "max_age=") {
		t.Errorf("after signing out, the sign-in isn't a fresh one: %s", loc)
	}
	// The sign-in's session from before doesn't stand in for it.
	w.is.reauth = false
	if r := b.signIn(rui); r.Code != http.StatusForbidden {
		t.Errorf("an old session after signing out: %d", r.Code)
	}
	w.is.reauth = true
	if r := b.signIn(tomas); r.Code != http.StatusSeeOther {
		t.Fatalf("signing in again: %d %s", r.Code, r.Body)
	}
	if loc := b.do("GET", "/auth/sign-in").Header().Get("Location"); strings.Contains(loc, "max_age=") {
		t.Errorf("once signed in again, still a fresh one: %s", loc)
	}
}

// Home's roles are the household account's groups, as idp names them.
func TestHomesRolesAreTheHouseholdAccountsGroups(t *testing.T) {
	testenv.Isolate(t)
	for _, r := range idp.Roles() {
		if got := home.Groups[string(r)]; got != idp.RoleGroup(r) {
			t.Errorf("%s: Home reads the group %q, the household account names it %q", r, got, idp.RoleGroup(r))
		}
	}
}

// Anyone may start a sign-in to Home (a mesh peer, an <img> on any app's page), as often as they like, and none of it
// stops the household's people from signing in (#21): a sign-in under way is carried by the browser's own sealed
// cookie, not a slot of Home's, and the sign-in's discovery document is read once, not at every start.
func TestStartingSignInsCantStopARealOne(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	for i := range 2000 {
		if r := w.browser().do("GET", "/auth/sign-in"); r.Code != http.StatusSeeOther {
			t.Fatalf("start %d: %d %s", i, r.Code, r.Body)
		}
	}
	if r := w.browser().signIn(rui); r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/" {
		t.Fatalf("Rui's sign-in after 2000 strangers' starts: %d %s", r.Code, r.Body)
	}
	if w.is.discovered != 1 {
		t.Errorf("2001 sign-ins read the discovery document %d times, want once", w.is.discovered)
	}
}

// A sign-in under way lasts ten minutes, from Home to the sign-in and back, whatever the browser keeps; and its cookie
// is Home's own: one Home can't open (made up, cut short, changed) is no sign-in, and the browser starts one over.
func TestASignInUnderWayLastsTenMinutes(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	start := w.now
	for _, c := range []struct {
		after time.Duration
		want  int
	}{{10*time.Minute - time.Second, http.StatusSeeOther}, {10 * time.Minute, http.StatusForbidden}} {
		w.now = start
		b := w.browser()
		rec := b.do("GET", "/auth/sign-in")
		w.now = start.Add(c.after) // the person signs in at the sign-in, and comes back
		state, code := w.is.code(t, rec.Header().Get("Location"), rui)
		if r := b.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code)); r.Code != c.want {
			t.Errorf("back %s after starting: %d %s, want %d", c.after, r.Code, r.Body, c.want)
		}
	}
	w.now = start
	b := w.browser()
	rec := b.do("GET", "/auth/sign-in")
	state, code := w.is.code(t, rec.Header().Get("Location"), rui)
	real := b.cookies["__Host-homerslab-home-sign-in"].Value
	for name, v := range map[string]string{"made up": "made-up", "cut short": real[:len(real)-1],
		"one byte changed": real[:10] + map[bool]string{true: "B", false: "A"}[real[10] == 'A'] + real[11:]} {
		b.cookies["__Host-homerslab-home-sign-in"] = &http.Cookie{Name: "__Host-homerslab-home-sign-in", Value: v}
		if r := b.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code)); r.Code != http.StatusSeeOther ||
			r.Header().Get("Location") != "/auth/sign-in" {
			t.Errorf("a sign-in cookie %s: %d %s %s, want it started over", name, r.Code, r.Header().Get("Location"), r.Body)
		}
	}
	// Without a sign-in cookie at all (a browser that keeps none), no sign-in, and no going round in circles.
	delete(b.cookies, "__Host-homerslab-home-sign-in")
	if r := b.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code)); r.Code != http.StatusForbidden {
		t.Errorf("no sign-in cookie: %d %s", r.Code, r.Body)
	}
}

// A sign-in that Home's restart cut off (its key went with the old process) starts over at the sign-in, which lets the
// person straight back; then they are in.
func TestASignInCutOffByARestartStartsOver(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	b := w.browser()
	rec := b.do("GET", "/auth/sign-in")
	state, code := w.is.code(t, rec.Header().Get("Location"), rui)
	w.restart()
	r := b.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code))
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/auth/sign-in" {
		t.Fatalf("back after a restart: %d %s %s, want it started over", r.Code, r.Header().Get("Location"), r.Body)
	}
	if r := b.signIn(rui); r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/" {
		t.Errorf("signing in again: %d %s", r.Code, r.Body)
	}
}

// Home serves an app's icon only as a picture: an icon of another type in the projection isn't served at all.
func TestHomeServesIconsOnlyAsPictures(t *testing.T) {
	testenv.Isolate(t)
	w := newWorld(t)
	p := theProjection(w.is)
	p.Apps[1].Icon = &home.Icon{Type: "text/html", Data: []byte("<script>alert(1)</script>")}
	w.write(p)
	b := w.browser()
	b.signIn(rui)
	if r := b.do("GET", "/api/v1/apps/bentopdf/icon"); r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("a picture's icon: %d %s", r.Code, r.Header().Get("Content-Type"))
	}
	if r := b.do("GET", "/api/v1/apps/donetick/icon"); r.Code != http.StatusNotFound || strings.Contains(r.Body.String(), "<script>") {
		t.Errorf("an HTML icon: %d %s %s", r.Code, r.Header().Get("Content-Type"), r.Body)
	}
}
