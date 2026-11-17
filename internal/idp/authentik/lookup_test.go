package authentik

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// fakeAuthentik is an Authentik that ignores every filter a list is asked with but a binding's target, as the real one ignores a filter its
// viewset doesn't declare (2026.8.5's proxy providers take name__iexact, never name: #219). Objects live per list
// endpoint; applications are also reached by slug.
type fakeAuthentik struct {
	mu      sync.Mutex
	objs    map[string][]map[string]any // by collection, e.g. /providers/proxy/
	next    int
	changes []string // every POST, PATCH and DELETE, as "METHOD path"
	after   func()   // called once each change is made
	// allBindings ignores the bindings' target filter too.
	allBindings bool
}

func newFake() *fakeAuthentik {
	f := &fakeAuthentik{objs: map[string][]map[string]any{}, next: 100}
	f.objs["/flows/instances/"] = []map[string]any{{"pk": "flow-consent", "slug": consentFlow}, {"pk": "flow-inval", "slug": invalidateFlow},
		{"pk": "flow-other", "slug": "default-authentication-flow"}}
	for _, r := range idp.Roles() {
		f.objs["/core/groups/"] = append(f.objs["/core/groups/"], map[string]any{"pk": "group-" + string(r), "name": idp.RoleGroup(r)})
	}
	f.objs["/core/groups/"] = append(f.objs["/core/groups/"], map[string]any{"pk": "group-authentik-admins", "name": "authentik Admins"})
	f.objs["/outposts/instances/"] = []map[string]any{{"pk": "outpost-ldap", "managed": nil, "providers": []int{}},
		{"pk": "outpost-embedded", "managed": embeddedOutpost, "providers": []int{}}}
	f.objs["/core/applications/"], f.objs["/policies/bindings/"], f.objs["/providers/proxy/"] = nil, nil, nil
	f.objs["/policies/expression/"] = []map[string]any{{"pk": "policy-other", "name": "default-source-enrollment-if-username"}}
	f.objs["/propertymappings/provider/scope/"] = []map[string]any{{"pk": "m-proxy", "name": "authentik default OAuth Mapping: Proxy outpost",
		"managed": "goauthentik.io/providers/proxy/scope-proxy", "scope_name": "ak_proxy"}}
	return f
}

func (f *fakeAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v3")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Method != http.MethodGet {
		f.changes = append(f.changes, r.Method+" "+p)
	}
	coll, id := p, ""
	if _, ok := f.objs[p]; !ok {
		i := strings.LastIndex(strings.TrimSuffix(p, "/"), "/")
		coll, id = p[:i+1], strings.Trim(p[i+1:], "/")
	}
	key := "pk"
	if coll == "/core/applications/" {
		key = "slug"
	}
	at := -1
	for i, o := range f.objs[coll] {
		if id != "" && fmt.Sprint(o[key]) == id {
			at = i
		}
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if f.after != nil && r.Method != http.MethodGet {
		defer f.after()
	}
	if coll == "/flows/bindings/" && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
		// Authentik keeps a flow's stage bindings unique by (target, stage, order).
		merged := map[string]any{}
		if at >= 0 {
			for k, v := range f.objs[coll][at] {
				merged[k] = v
			}
		}
		for k, v := range body {
			merged[k] = v
		}
		for i, o := range f.objs[coll] {
			if i != at && fmt.Sprint(o["target"], o["stage"], o["order"]) == fmt.Sprint(merged["target"], merged["stage"], merged["order"]) {
				w.WriteHeader(http.StatusBadRequest)
				reply(map[string]any{"non_field_errors": []string{"The fields target, stage, order must make a unique set."}})
				return
			}
		}
	}
	switch {
	case id == "" && r.Method == http.MethodGet && coll == "/policies/bindings/" && !f.allBindings:
		// The one filter it honours: a binding answers its target as the target's own pk, so the adapter can't check it.
		var bs []map[string]any
		for _, b := range f.objs[coll] {
			if b["target"] == r.URL.Query().Get("target") {
				bs = append(bs, b)
			}
		}
		reply(map[string]any{"pagination": map[string]any{"next": 0}, "results": bs})
	case id == "" && r.Method == http.MethodGet:
		reply(map[string]any{"pagination": map[string]any{"next": 0}, "results": f.objs[coll]})
	case id == "" && r.Method == http.MethodPost:
		f.next++
		var pk any = fmt.Sprintf("obj-%d", f.next)
		if coll == "/providers/proxy/" {
			pk = f.next
		}
		body["pk"] = pk
		if coll == "/core/applications/" { // an application's pk is its policy binding model's, as in Authentik
			body["pbm_uuid"] = fmt.Sprintf("app-%d", f.next)
			body["pk"] = body["pbm_uuid"]
		}
		if coll == "/flows/bindings/" { // a stage binding is a target of policy bindings too
			body["policybindingmodel_ptr_id"] = fmt.Sprintf("pbm-%d", f.next)
		}
		f.objs[coll] = append(f.objs[coll], body)
		reply(body)
	case at < 0:
		http.NotFound(w, r)
	case r.Method == http.MethodGet:
		reply(f.objs[coll][at])
	case r.Method == http.MethodPatch:
		for k, v := range body {
			f.objs[coll][at][k] = v
		}
		reply(f.objs[coll][at])
	case r.Method == http.MethodDelete:
		f.objs[coll] = append(f.objs[coll][:at], f.objs[coll][at+1:]...)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeAuthentik) open(t *testing.T) *Authentik {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h := idp.Household{Name: "x", Domain: "example.test"}
	a, err := newAPI(srv.URL, h, "token-PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	return &Authentik{api: a, h: h}
}

func (f *fakeAuthentik) providers() map[string]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[string]any{}
	for _, p := range f.objs["/providers/proxy/"] {
		out[p["name"].(string)] = p
	}
	return out
}

var (
	pdf    = idp.Guard{Slug: "homerslab-bentopdf", Name: "BentoPDF", URL: "https://pdf.example.test", Roles: []idp.Role{idp.Admin, idp.Adult}}
	chores = idp.Guard{Slug: "homerslab-chores", Name: "Chores", URL: "https://chores.example.test", Roles: []idp.Role{idp.Kid}}
)

// Two guarded apps, on an Authentik that ignores the lookup's filter: each gets a provider of its own at its own URL,
// its application bound to it, opened only by its own roles; making either again changes neither (#219).
func TestASecondGuardKeepsTheFirstsProvider(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	a := f.open(t)
	ctx := context.Background()
	for _, g := range []idp.Guard{pdf, chores, pdf, chores} {
		if err := a.EnsureGuard(ctx, g); err != nil {
			t.Fatalf("%s: %v", g.Slug, err)
		}
	}
	ps := f.providers()
	if len(ps) != 2 {
		t.Fatalf("providers: %v, want one per app", ps)
	}
	for _, g := range []idp.Guard{pdf, chores} {
		p := ps[g.Slug]
		if p == nil || p["external_host"] != g.URL {
			t.Errorf("%s's provider: %v, want its own at %s", g.Slug, p, g.URL)
			continue
		}
		var app map[string]any
		for _, o := range f.objs["/core/applications/"] {
			if o["slug"] == g.Slug {
				app = o
			}
		}
		if app == nil || fmt.Sprint(app["provider"]) != fmt.Sprint(p["pk"]) {
			t.Errorf("%s's application: %v, want it on provider %v", g.Slug, app, p["pk"])
			continue
		}
		var groups []string
		for _, b := range f.objs["/policies/bindings/"] {
			if b["target"] == app["pbm_uuid"] {
				groups = append(groups, fmt.Sprint(b["group"]))
			}
		}
		var want []string
		for _, r := range g.Roles {
			want = append(want, "group-"+string(r))
		}
		if strings.Join(groups, ",") != strings.Join(want, ",") {
			t.Errorf("%s opens to %v, want %v", g.Slug, groups, want)
		}
	}
}

// A provider found under the guard's name but at another app's URL, or already another application's, is never
// rewritten: the guard is refused instead (#219).
func TestAGuardNeverRewritesAnotherAppsProvider(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name string
		have map[string]any
	}{
		{"at another URL", map[string]any{"pk": 7, "name": chores.Slug, "external_host": pdf.URL, "assigned_application_slug": nil}},
		{"another application's", map[string]any{"pk": 7, "name": chores.Slug, "external_host": chores.URL, "assigned_application_slug": pdf.Slug}},
	} {
		f := newFake()
		f.objs["/providers/proxy/"] = []map[string]any{c.have}
		a := f.open(t)
		if err := a.EnsureGuard(context.Background(), chores); err == nil {
			t.Errorf("%s: the guard was made", c.name)
		}
		for _, ch := range f.changes {
			if strings.Contains(ch, "/providers/proxy/") || strings.Contains(ch, "/core/applications/") || strings.Contains(ch, "/outposts/") {
				t.Errorf("%s: %s", c.name, ch)
			}
		}
	}
	// Its own application's, at the app's old URL (a new domain or port), follows the app.
	f := newFake()
	f.objs["/providers/proxy/"] = []map[string]any{{"pk": 7, "name": chores.Slug, "external_host": "https://chores.old.test", "assigned_application_slug": chores.Slug}}
	if err := f.open(t).EnsureGuard(context.Background(), chores); err != nil {
		t.Errorf("its own provider, at its old URL: %v", err)
	}
	if p := f.providers()[chores.Slug]; p["external_host"] != chores.URL {
		t.Errorf("its provider: %v, want it at %s", p, chores.URL)
	}
	// Its own, unassigned yet (a guard made halfway before), is brought in line.
	f = newFake()
	f.objs["/providers/proxy/"] = []map[string]any{{"pk": 7, "name": chores.Slug, "external_host": chores.URL, "assigned_application_slug": nil}}
	if err := f.open(t).EnsureGuard(context.Background(), chores); err != nil {
		t.Errorf("its own provider, unassigned: %v", err)
	}
	if ps := f.providers(); len(ps) != 1 {
		t.Errorf("providers: %v, want its own, brought in line", ps)
	}
}

// find is the one object whose identifying field is exactly the value, whatever else Authentik sent: a filter it
// ignored, or one that matches without regard to case (#219).
func TestFindKeepsOnlyTheExactMatch(t *testing.T) {
	testenv.Isolate(t)
	var results []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": 0}, "results": results})
	}))
	defer srv.Close()
	a, err := newAPI(srv.URL, idp.Household{Name: "x", Domain: "example.test"}, "token-PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	type obj struct {
		PK   int    `json:"pk"`
		Name string `json:"name"`
	}
	ctx := context.Background()
	results = []map[string]any{{"pk": 1, "name": "homerslab-bentopdf"}, {"pk": 2, "name": "Homeward-Chores"}, {"pk": 3, "name": "homerslab-chores"}, {"pk": 4}}
	if o, err := find[obj](ctx, a, "/providers/proxy/?name__iexact=homerslab-chores", "name", "homerslab-chores"); err != nil || o == nil || o.PK != 3 {
		t.Errorf("found %+v, %v: want pk 3", o, err)
	}
	if o, err := find[obj](ctx, a, "/providers/proxy/", "name", "homerslab-notes"); err != nil || o != nil {
		t.Errorf("found %+v, %v: want none", o, err)
	}
	results = append(results, map[string]any{"pk": 5, "name": "homerslab-chores"})
	if o, err := find[obj](ctx, a, "/providers/proxy/", "name", "homerslab-chores"); err == nil {
		t.Errorf("found %+v of two", o)
	}
	results = []map[string]any{{"pk": 6, "name": 6}}
	if o, err := find[obj](ctx, a, "/providers/proxy/", "name", "6"); err != nil || o != nil {
		t.Errorf("a number for a name: %+v, %v", o, err)
	}
}

// An app's audience changes with its bindings added before the old ones go: at no point between two calls to Authentik
// is the application without a binding (open to everyone), or bound to a group of neither audience.
func TestAnAudienceChangeNeverLeavesTheAppOpen(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	a := f.open(t)
	ctx := context.Background()
	g := chores
	if err := a.EnsureGuard(ctx, g); err != nil {
		t.Fatal(err)
	}
	var app string
	for _, o := range f.objs["/core/applications/"] {
		app = fmt.Sprint(o["pbm_uuid"])
	}
	// Each change Authentik is asked for is checked once it is made.
	f.after = func() {
		var groups []string
		for _, b := range f.objs["/policies/bindings/"] {
			if b["target"] == app {
				groups = append(groups, fmt.Sprint(b["group"], b["policy"]))
			}
		}
		if len(groups) == 0 {
			t.Errorf("after %s: the application has no binding: open to everyone", f.changes[len(f.changes)-1])
		}
		for _, gr := range groups {
			if gr != "group-kid<nil>" && gr != "group-adult<nil>" && gr != "group-admin<nil>" {
				t.Errorf("after %s: bound to %s", f.changes[len(f.changes)-1], gr)
			}
		}
	}
	for _, roles := range [][]idp.Role{{idp.Adult}, {idp.Admin, idp.Adult}, {idp.Kid}} {
		g.Roles = roles
		if err := a.EnsureGuard(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	f.after = nil
	var groups []string
	for _, b := range f.objs["/policies/bindings/"] {
		if b["target"] == app {
			groups = append(groups, fmt.Sprint(b["group"]))
		}
	}
	if strings.Join(groups, ",") != "group-kid" {
		t.Errorf("bound to %v at the end, want only the kids' group", groups)
	}
}

// An OIDC client's provider, found by its name but another application's, is neither rewritten nor deleted (#219).
func TestAClientNeverTouchesAnotherApplicationsProvider(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	f.objs["/providers/oauth2/"] = []map[string]any{{"pk": 7, "name": "homerslab-other", "assigned_application_slug": "homerslab-other"},
		{"pk": 8, "name": "homerslab-mealie", "assigned_application_slug": "homerslab-other"}}
	f.objs["/propertymappings/provider/scope/"] = []map[string]any{{"pk": "m1", "managed": "goauthentik.io/providers/oauth2/scope-openid"},
		{"pk": "m2", "managed": "goauthentik.io/providers/oauth2/scope-email"}, {"pk": "m3", "managed": "goauthentik.io/providers/oauth2/scope-profile"}}
	f.objs["/crypto/certificatekeypairs/"] = []map[string]any{{"pk": "c1", "name": selfSignedCert}}
	// Its own application is there too: refusing comes before anything is deleted.
	f.objs["/core/applications/"] = []map[string]any{{"slug": "homerslab-mealie", "pbm_uuid": "app-m", "provider_obj": map[string]any{"meta_model_name": clientProviderModel}}}
	a := f.open(t)
	ctx := context.Background()
	if _, err := a.EnsureClient(ctx, idp.Client{Slug: "homerslab-mealie", Name: "Mealie", RedirectURIs: []string{"https://meals.example.test/cb"},
		Roles: []idp.Role{idp.Adult}}); err == nil {
		t.Error("the client was made on another application's provider")
	}
	if err := a.DeleteClient(ctx, "homerslab-mealie"); err == nil {
		t.Error("another application's provider was deleted")
	}
	for _, ch := range f.changes {
		if strings.Contains(ch, "/providers/") || strings.Contains(ch, "/core/") {
			t.Error(ch)
		}
	}
}

// Bindings of another target in the answer (Authentik ignoring the target filter) are never touched: deleting them
// would leave every other application unbound, open to everyone (#219).
func TestBindingsOfAnotherTargetAreNeverTouched(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	a := f.open(t)
	ctx := context.Background()
	if err := a.EnsureGuard(ctx, pdf); err != nil {
		t.Fatal(err)
	}
	f.allBindings, f.changes = true, nil
	if err := a.EnsureGuard(ctx, chores); err != nil {
		t.Fatal(err)
	}
	for _, ch := range f.changes {
		if strings.HasPrefix(ch, "DELETE /policies/bindings/") || strings.HasPrefix(ch, "PATCH /policies/bindings/") {
			t.Error(ch)
		}
	}
}

// Every guard's provider carries Homeward's one role mapping (#236): it names the role in its own header from the role
// groups' exact names, so the edge never reads a group's name; its scope sorts after Authentik's own proxy mapping,
// whose attributes it would otherwise lose to. Made once, whatever the number of guards.
func TestEveryGuardCarriesTheRoleMapping(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	a := f.open(t)
	for _, g := range []idp.Guard{pdf, chores, pdf} {
		if err := a.EnsureGuard(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
	var mine []map[string]any
	for _, m := range f.objs["/propertymappings/provider/scope/"] {
		if m["name"] == roleMapping {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("role mappings: %v, want one", mine)
	}
	m := mine[0]
	expr, _ := m["expression"].(string)
	if scope, _ := m["scope_name"].(string); scope <= "ak_proxy" {
		t.Errorf("its scope %q sorts before ak_proxy's", scope)
	}
	for _, want := range []string{`"homerslab-admins": "admin"`, `"homerslab-adults": "adult"`, `"homerslab-kids": "kid"`, "user.groups.filter(name__in=",
		"if len(mine) == 1 and not forged else", `headers["` + RoleHeader + `"] = role`,
		`.lower().replace("_", "-").startswith(("x-authentik-", "x-homerslab-"))`, `headers = {k: "" for k in forged}`} {
		if !strings.Contains(expr, want) {
			t.Errorf("its expression lacks %s:\n%s", want, expr)
		}
	}
	for name, p := range f.providers() {
		if ms, _ := p["property_mappings"].([]any); len(ms) != 1 || ms[0] != m["pk"] {
			t.Errorf("%s's provider carries %v, want the role mapping %v", name, p["property_mappings"], m["pk"])
		}
	}
}
