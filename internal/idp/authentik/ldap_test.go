package authentik_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/idp/authentik"
	"github.com/ArjunRachithcf/homeward/internal/idp/idptest"
	"github.com/ArjunRachithcf/homeward/internal/ldaps/ldaptest"
)

// The LDAP outpost of the packaged compose file (service ldap, profile ldap), at its address in the test's range.
var outpostLDAP = signInNet + ".11:3389"

func (w *world) LDAP() idp.LDAP { return w.provider }

// StartOutpost does what the sign-in's unit does once an app signs in over LDAP: the outpost's token in its file, then
// the compose file's ldap profile.
func (w *world) StartOutpost(t *testing.T, token idp.Secret) {
	t.Helper()
	f := filepath.Join(w.dir, "ldap-token")
	_ = os.Remove(f)
	if err := os.WriteFile(f, []byte(token.Reveal()), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		if out, err := exec.Command("chcon", "-t", "container_file_t", "-l", "s0:c1000,c1001", f).CombinedOutput(); err != nil {
			t.Fatalf("labelling the outpost's token: %v %s", err, out)
		}
	}
	// Recreated: the outpost reads its token only as it starts, as the unit restarts it.
	w.compose(t, "--profile", "ldap", "up", "-d", "--quiet-pull", "--force-recreate", "--no-deps", "ldap")
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		c, err := net.DialTimeout("tcp", outpostLDAP, 2*time.Second)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the LDAP outpost never listened: %v", err)
		}
	}
}

// appPassword is a new app password of a person's for one LDAP app, made by the adapter as Home will make it.
func appPassword(t *testing.T, a *authentik.Authentik, slug, username string) string {
	t.Helper()
	pw, err := a.AppPassword(context.Background(), slug, username)
	if err != nil || pw == "" {
		t.Fatalf("an app password for %s at %s: %v", username, slug, err)
	}
	return pw.Reveal()
}

func (w *world) Search(t *testing.T, dn, password, base string) ([]ldaptest.Entry, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", outpostLDAP, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return ldaptest.Search(c, dn, password, base)
}

func (w *world) Bind(t *testing.T, dn, password string) error {
	t.Helper()
	c, err := net.DialTimeout("tcp", outpostLDAP, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return ldaptest.Bind(c, dn, password)
}

// ldapFlowOnlyAtTheOutpost checks that a browser can't run an app's bind flow: an app password signs nobody in to the
// provider itself.
func ldapFlowOnlyAtTheOutpost(t *testing.T, flow string) {
	req, _ := http.NewRequest("GET", upstream+"/api/v3/flows/executor/"+flow+"/?query=", nil)
	req.Host = household.SignIn()
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ch struct {
		Component string `json:"component"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ch)
	if ch.Component != "ak-stage-access-denied" {
		t.Errorf("the bind flow, from a browser: %d %q, want access denied", resp.StatusCode, ch.Component)
	}
}

// ldapDrift opens an app's bind flow to passwords, or to another app's app passwords, behind Homeward's back, as an
// admin in Authentik's console could: the report says the sign-in is no longer passkey-only, and Prepare puts the flow
// back.
func ldapDrift(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
		app := idp.LDAPApp{Slug: "homerslab-drift", Name: "Drift", Roles: []idp.Role{idp.Adult}}
		if _, err := a.EnsureLDAP(ctx, app); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := a.DeleteLDAP(ctx, app.Slug); err != nil {
				t.Error(err)
			}
		}()
		flow := authentik.LDAPBindFlow(app.Slug)
		t.Run("the bind flow runs only at the outpost, never in a browser", func(t *testing.T) { ldapFlowOnlyAtTheOutpost(t, flow) })
		var st struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/stages/password/?name=homerslab-ldap-app-password", nil, &st)
		if len(st.Results) != 1 {
			t.Fatalf("the bind flow's password stage: %+v", st.Results)
		}
		w.raw(t, w.own, "PATCH", "/api/v3/stages/password/"+st.Results[0].PK+"/",
			map[string]any{"backends": []string{"authentik.core.auth.InbuiltBackend", "authentik.core.auth.TokenBackend"}}, nil)
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow that takes passwords: %+v %v, want not passkey-only", r, err)
		}
		w.raw(t, w.own, "PATCH", "/api/v3/flows/instances/"+flow+"/", map[string]any{"authentication": "none"}, nil)
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare: %+v %v", r, err)
		}
		ldapFlowOnlyAtTheOutpost(t, flow)
		// The app password's stage taken out of the flow: a username alone would bind.
		var bs struct {
			Results []struct {
				PK    string `json:"pk"`
				Stage string `json:"stage"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+flowPK(t, w, flow), nil, &bs)
		for _, b := range bs.Results {
			if b.Stage == st.Results[0].PK {
				w.raw(t, w.own, "DELETE", "/api/v3/flows/bindings/"+b.PK+"/", nil, nil)
			}
		}
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow without its password stage: %+v %v, want not passkey-only", r, err)
		}
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare, again: %+v %v", r, err)
		}
		// A policy on the password stage's binding: one that fails would skip the stage, and bind anyone.
		var never struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/policies/expression/?name=homerslab-never", nil, &never)
		var fsb struct {
			Results []struct {
				Stage string `json:"stage"`
				PBM   string `json:"policybindingmodel_ptr_id"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+flowPK(t, w, flow), nil, &fsb)
		for _, b := range fsb.Results {
			if b.Stage == st.Results[0].PK && len(never.Results) == 1 {
				w.raw(t, w.own, "POST", "/api/v3/policies/bindings/", map[string]any{"target": b.PBM, "policy": never.Results[0].PK, "order": 0}, nil)
			}
		}
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow with a policy on its password stage: %+v %v, want not passkey-only", r, err)
		}
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare, a third time: %+v %v", r, err)
		}
		// The app's own app-password policy taken off its session stage, or made to let every app password through:
		// another app's app password would bind here.
		var own struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/policies/expression/?name="+app.Slug+"-ldap-app-password", nil, &own)
		if len(own.Results) != 1 {
			t.Fatalf("the app's own app-password policy: %+v", own.Results)
		}
		var pbs struct {
			Results []struct {
				PK     string `json:"pk"`
				Policy string `json:"policy"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/policies/bindings/?policy="+own.Results[0].PK, nil, &pbs)
		for _, b := range pbs.Results {
			if b.Policy == own.Results[0].PK {
				w.raw(t, w.own, "DELETE", "/api/v3/policies/bindings/"+b.PK+"/", nil, nil)
			}
		}
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow whose session stage takes any app password: %+v %v, want not passkey-only", r, err)
		}
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare, a fourth time: %+v %v", r, err)
		}
		w.raw(t, w.own, "PATCH", "/api/v3/policies/expression/"+own.Results[0].PK+"/", map[string]any{"expression": "return True"}, nil)
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow whose app-password policy lets everyone through: %+v %v, want not passkey-only", r, err)
		}
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare, a fifth time: %+v %v", r, err)
		}
		t.Run("a session stage left refusing by a failure halfway is put back by the next pass", func(t *testing.T) { ldapHalfDone(t, w, a, app) })
		t.Run("one app's bind flow that can't be put back keeps Prepare from nothing else, the first part's bind flow going among it", func(t *testing.T) { ldapOneStuck(t, w, a) })
	}
}

// ldapHalfDone leaves an app's session stage as a failure (a 503, a restart) between the two halves of making its
// binding leaves it (#246's review): the binding there, at the session's order, holding the stage that refuses. The
// app binds nobody then; the next pass takes that binding up, and the app binds again.
func ldapHalfDone(t *testing.T, w *world, a *authentik.Authentik, app idp.LDAPApp) {
	ctx := context.Background()
	type pks struct {
		Results []struct {
			PK string `json:"pk"`
		}
	}
	var deny, login pks
	w.raw(t, w.own, "GET", "/api/v3/stages/deny/?name=homerslab-passkey-only", nil, &deny)
	w.raw(t, w.own, "GET", "/api/v3/stages/user_login/?name=homerslab-ldap-session", nil, &login)
	if len(deny.Results) != 1 || len(login.Results) != 1 {
		t.Fatalf("the stage that refuses %+v, the session's %+v", deny.Results, login.Results)
	}
	var bs struct {
		Results []struct {
			PK    string `json:"pk"`
			Stage string `json:"stage"`
		}
	}
	w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+flowPK(t, w, authentik.LDAPBindFlow(app.Slug)), nil, &bs)
	left := false
	for _, b := range bs.Results {
		if b.Stage == login.Results[0].PK {
			w.raw(t, w.own, "PATCH", "/api/v3/flows/bindings/"+b.PK+"/", map[string]any{"stage": deny.Results[0].PK}, nil)
			left = true
		}
	}
	if !left {
		t.Fatalf("the bind flow has no session stage: %+v", bs.Results)
	}
	if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
		t.Errorf("a bind flow left halfway: %+v %v, want not passkey-only", r, err)
	}
	// The hourly pass's way (EnsureLDAP's own, with the outpost's tidy once for every app).
	if err := a.EnsureLDAPApps(ctx, []idp.LDAPApp{app}); err != nil {
		t.Fatalf("EnsureLDAPApps after a binding left halfway: %v", err)
	}
	if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
		t.Errorf("after EnsureLDAP: %+v %v", r, err)
	}
	pw := appPassword(t, a, app.Slug, "lena")
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(3 * time.Second) {
		err := w.Bind(t, fmt.Sprintf(authentik.LDAPUserDN(app.Slug), "lena"), pw)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lena, with her app password, after the bind flow was put back: %v", err)
		}
	}
}

// ldapOneStuck makes one app's bind flow impossible to put back (the name of its app-password policy taken by another
// policy, from the console) and changes the sign-in's brand: Prepare says the one app failed, and still puts the brand
// back, as everything after the bind flows (#246's review). The first part's one bind flow goes, before any of it, and no
// provider is left on it.
func ldapOneStuck(t *testing.T, w *world, a *authentik.Authentik) {
	ctx := context.Background()
	var flow, policy struct {
		PK string `json:"pk"`
	}
	w.raw(t, w.own, "POST", "/api/v3/flows/instances/", map[string]any{"name": "Stuck", "slug": "homerslab-stuck-ldap-bind", "title": "Stuck",
		"designation": "authentication"}, &flow)
	defer func() {
		if w.status(t, w.own, "/api/v3/flows/instances/homerslab-stuck-ldap-bind/") == http.StatusOK {
			w.raw(t, w.own, "DELETE", "/api/v3/flows/instances/homerslab-stuck-ldap-bind/", nil, nil)
		}
	}()
	w.raw(t, w.own, "POST", "/api/v3/policies/dummy/", map[string]any{"name": "homerslab-stuck-ldap-app-password", "result": false}, &policy)
	defer w.raw(t, w.own, "DELETE", "/api/v3/policies/dummy/"+policy.PK+"/", nil, nil)
	type brand struct {
		UUID   string `json:"brand_uuid"`
		Domain string `json:"domain"`
		Title  string `json:"branding_title"`
	}
	signInBrand := func() brand {
		var bs struct{ Results []brand }
		w.raw(t, w.own, "GET", "/api/v3/core/brands/?domain="+household.SignIn(), nil, &bs)
		for _, b := range bs.Results {
			if b.Domain == household.SignIn() {
				return b
			}
		}
		t.Fatalf("no brand of the sign-in's: %+v", bs.Results)
		return brand{}
	}
	w.raw(t, w.own, "PATCH", "/api/v3/core/brands/"+signInBrand().UUID+"/", map[string]any{"branding_title": "Drifted"}, nil)
	// The one bind flow of LDAP's first part, which took any app's app password, with a provider of the console's on it.
	var old, inval struct {
		PK string `json:"pk"`
	}
	w.raw(t, w.own, "POST", "/api/v3/flows/instances/", map[string]any{"name": "Old", "slug": "homerslab-ldap-bind", "title": "LDAP",
		"designation": "authentication"}, &old)
	w.raw(t, w.own, "GET", "/api/v3/flows/instances/default-provider-invalidation-flow/", nil, &inval)
	var prov struct {
		PK int `json:"pk"`
	}
	w.raw(t, w.own, "POST", "/api/v3/providers/ldap/", map[string]any{"name": "part-one", "authorization_flow": old.PK,
		"invalidation_flow": inval.PK, "base_dn": "dc=part-one,dc=homerslab"}, &prov)
	defer func() {
		if w.status(t, w.own, fmt.Sprintf("/api/v3/providers/ldap/%d/", prov.PK)) == http.StatusOK {
			w.raw(t, w.own, "DELETE", fmt.Sprintf("/api/v3/providers/ldap/%d/", prov.PK), nil, nil)
		}
	}()
	err := idptest.Prepare(ctx, a, household)
	if err == nil || !strings.Contains(err.Error(), "homerslab-stuck-ldap") {
		t.Errorf("Prepare with one app's bind flow stuck: %v, want that app's error", err)
	}
	if b := signInBrand(); b.Title != household.Name {
		t.Errorf("the sign-in's brand after Prepare: %q, want %q: Prepare stopped at the stuck bind flow", b.Title, household.Name)
	}
	if code := w.status(t, w.own, "/api/v3/flows/instances/homerslab-ldap-bind/"); code != http.StatusNotFound {
		t.Errorf("the first part's bind flow after Prepare: %d, want gone", code)
	}
	var p struct {
		Flow *string `json:"authorization_flow"`
	}
	if code, b := w.do(t, w.own, "GET", fmt.Sprintf("/api/v3/providers/ldap/%d/", prov.PK), nil); code == http.StatusOK {
		if err := json.Unmarshal(b, &p); err != nil || p.Flow != nil && *p.Flow == old.PK {
			t.Errorf("a provider still on the first part's bind flow: %s %v", b, err)
		}
	}
}

func flowPK(t *testing.T, w *world, slug string) string {
	var f struct {
		PK string `json:"pk"`
	}
	w.raw(t, w.own, "GET", "/api/v3/flows/instances/"+slug+"/", nil, &f)
	return f.PK
}

// ldapBesideTheAppsOwn checks that an app's directory keeps apart from the app's own application, homerslab-<app> (its
// OIDC client's or its guard's): made beside it, and deleted, it leaves it as it was.
func ldapBesideTheAppsOwn(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
		w.raw(t, w.own, "POST", "/api/v3/core/applications/", map[string]any{"name": "Books", "slug": "homerslab-books"}, nil)
		app := idp.LDAPApp{Slug: "homerslab-books", Name: "Books", Roles: []idp.Role{idp.Adult}}
		if _, err := a.EnsureLDAP(ctx, app); err != nil {
			t.Fatalf("the directory, beside the app's own application: %v", err)
		}
		if err := a.DeleteLDAP(ctx, app.Slug); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Name     string `json:"name"`
			Provider *int   `json:"provider"`
		}
		w.raw(t, w.own, "GET", "/api/v3/core/applications/homerslab-books/", nil, &got)
		if got.Name != "Books" || got.Provider != nil {
			t.Errorf("the app's own application, after its directory came and went: %+v", got)
		}
	}
}

// ldapOrphan deletes an app's LDAP application behind Homeward's back, as an admin in Authentik's console could, while
// the outpost serves its provider: the report says so, and Prepare closes it to nobody at once and takes the provider
// off the outpost (orphaned providers are removed). A later pass deletes them.
func ldapOrphan(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
		app := idp.LDAPApp{Slug: "homerslab-orphan", Name: "Orphan", Roles: []idp.Role{idp.Adult}}
		creds, err := a.EnsureLDAP(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		pw := map[string]string{}
		for _, who := range []string{"lena", "kiko", "olga"} {
			pw[who] = appPassword(t, a, app.Slug, who)
		}
		bind := func(who string) error { return w.Bind(t, fmt.Sprintf(creds.UserDN, who), pw[who]) }
		for deadline := time.Now().Add(90 * time.Second); bind("lena") != nil; time.Sleep(3 * time.Second) {
			if time.Now().After(deadline) {
				t.Fatal("the outpost never served the app's provider")
			}
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Fatalf("before: %+v %v", r, err)
		}
		w.raw(t, w.own, "DELETE", "/api/v3/core/applications/homerslab-orphan-ldap/", nil, nil)
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a provider at the outpost without its application: %+v %v, want not passkey-only", r, err)
		}
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
			for _, who := range []string{"lena", "kiko", "olga"} {
				if bind(who) == nil {
					t.Fatalf("%s bound to an orphaned provider after Prepare", who)
				}
			}
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare: %+v %v", r, err)
		}
		var o struct {
			Results []struct {
				Providers []int `json:"providers"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/outposts/instances/?name__iexact="+authentik.LDAPOutpostName, nil, &o)
		var p struct {
			Results []struct {
				PK   int    `json:"pk"`
				Name string `json:"name"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/providers/ldap/?name__iexact=homerslab-orphan-ldap", nil, &p)
		if len(o.Results) != 1 || len(p.Results) != 1 {
			t.Fatalf("the outpost %+v, the provider %+v", o.Results, p.Results)
		}
		for _, pk := range o.Results[0].Providers {
			if pk == p.Results[0].PK {
				t.Error("the orphaned provider is still at the outpost")
			}
		}
		// The later pass: once off the outpost long enough (here at once), the closed application and its provider go.
		authentik.SetLDAPRetire(a, 0)
		defer authentik.SetLDAPRetire(a, 5*time.Minute)
		if err := idptest.Prepare(ctx, a, household); err != nil {
			t.Fatal(err)
		}
		if code := w.status(t, w.own, "/api/v3/core/applications/homerslab-orphan-ldap/"); code != http.StatusNotFound {
			t.Errorf("the closed application, after the later pass: %d", code)
		}
		if code := w.status(t, w.own, fmt.Sprintf("/api/v3/providers/ldap/%d/", p.Results[0].PK)); code != http.StatusNotFound {
			t.Errorf("the provider, after the later pass: %d", code)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after the later pass: %+v %v", r, err)
		}
	}
}

// ldapImpostors puts providers at the outpost behind Homeward's back, as an admin in Authentik's console could, named
// as an app's and with an application of their own that has no binding: one with a copy of a real app's base DN, one
// with a base DN of its own. The report says so; Prepare takes them off the outpost, and nobody binds through them.
// The outpost asks for its providers every minute, not only when told.
func ldapImpostors(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
		var cfg struct {
			Results []struct {
				PK        string         `json:"pk"`
				Providers []int          `json:"providers"`
				Config    map[string]any `json:"config"`
			}
		}
		outpost := func() (string, []int) {
			w.raw(t, w.own, "GET", "/api/v3/outposts/instances/?name__iexact="+authentik.LDAPOutpostName, nil, &cfg)
			if len(cfg.Results) != 1 {
				t.Fatalf("the outpost: %+v", cfg.Results)
			}
			return cfg.Results[0].PK, cfg.Results[0].Providers
		}
		outpost()
		if got := cfg.Results[0].Config["refresh_interval"]; got != "minutes=1" {
			t.Errorf("the outpost's refresh_interval: %v, want minutes=1", got)
		}
		real := idp.LDAPApp{Slug: "homerslab-real", Name: "Real", Roles: []idp.Role{idp.Adult}}
		creds, err := a.EnsureLDAP(ctx, real)
		if err != nil {
			t.Fatal(err)
		}
		pw := map[string]string{}
		for _, who := range []string{"lena", "kiko", "olga"} {
			pw[who] = appPassword(t, a, real.Slug, who)
		}
		// Another adults' app, whose app passwords open it alone.
		other := idp.LDAPApp{Slug: "homerslab-other", Name: "Other", Roles: []idp.Role{idp.Adult}}
		if _, err := a.EnsureLDAP(ctx, other); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.DeleteLDAP(context.Background(), other.Slug) }()
		lenaOther := appPassword(t, a, other.Slug, "lena")
		bindAt := func(userDN, who string) error { return w.Bind(t, fmt.Sprintf(userDN, who), pw[who]) }
		waitFor := func(userDN string) {
			t.Helper()
			for deadline := time.Now().Add(150 * time.Second); bindAt(userDN, "lena") != nil; time.Sleep(3 * time.Second) {
				if time.Now().After(deadline) {
					t.Fatalf("the outpost never served %s", userDN)
				}
			}
		}
		waitFor(creds.UserDN)
		var inval struct {
			Results []struct {
				PK string `json:"pk"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/flows/instances/?slug=default-provider-invalidation-flow", nil, &inval)
		if len(inval.Results) != 1 {
			t.Fatalf("the invalidation flow: %+v", inval.Results)
		}
		groupPK := func(name string) string {
			var g struct {
				Results []struct {
					PK   string `json:"pk"`
					Name string `json:"name"`
				}
			}
			w.raw(t, w.own, "GET", "/api/v3/core/groups/?name="+name, nil, &g)
			for _, x := range g.Results {
				if x.Name == name {
					return x.PK
				}
			}
			t.Fatalf("no group %s", name)
			return ""
		}
		kids := groupPK(idp.RoleGroup(idp.Kid))
		// provider puts a provider of the console's at the outpost, first, with an application of its own that the
		// given bindings open (none: open to everyone).
		provider := func(t *testing.T, name, baseDN string, binds []map[string]any) (int, string) {
			var p struct {
				PK int `json:"pk"`
			}
			// The real app's own bind flow: the console's copy takes the real app's app passwords.
			w.raw(t, w.own, "POST", "/api/v3/providers/ldap/", map[string]any{"name": name, "authorization_flow": flowPK(t, w, authentik.LDAPBindFlow(real.Slug)),
				"invalidation_flow": inval.Results[0].PK, "base_dn": baseDN, "bind_mode": "direct", "search_mode": "direct"}, &p)
			var app struct {
				PBM string `json:"pbm_uuid"`
			}
			w.raw(t, w.own, "POST", "/api/v3/core/applications/", map[string]any{"name": name, "slug": name, "provider": p.PK}, &app)
			for i, b := range binds {
				b["target"], b["order"] = app.PBM, i*10
				w.raw(t, w.own, "POST", "/api/v3/policies/bindings/", b, nil)
			}
			pk, ps := outpost()
			w.raw(t, w.own, "PATCH", "/api/v3/outposts/instances/"+pk+"/", map[string]any{"providers": append([]int{p.PK}, ps...)}, nil)
			return p.PK, "cn=%s,ou=users," + baseDN
		}
		// binding gives the real app's application one more binding of the console's.
		binding := func(t *testing.T, b map[string]any) {
			var app struct {
				PBM string `json:"pbm_uuid"`
			}
			w.raw(t, w.own, "GET", "/api/v3/core/applications/"+real.Slug+"-ldap/", nil, &app)
			b["target"], b["order"] = app.PBM, 100
			w.raw(t, w.own, "POST", "/api/v3/policies/bindings/", b, nil)
		}
		var realP struct {
			Results []struct {
				PK   int    `json:"pk"`
				Name string `json:"name"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/providers/ldap/?name__iexact="+real.Slug+"-ldap", nil, &realP)
		if len(realP.Results) != 1 {
			t.Fatalf("the real app's provider: %+v", realP.Results)
		}
		for _, c := range []struct {
			name      string
			drift     func(t *testing.T) (int, string) // the console's provider and its DN template, if it made one
			realStays bool                             // the real app's provider stays at the outpost through Prepare
		}{
			{"a copy of the real app's base DN", func(t *testing.T) (int, string) {
				return provider(t, "homerslab-evil-ldap", authentik.LDAPBaseDN(real.Slug), nil)
			}, true},
			{"a base DN of its own", func(t *testing.T) (int, string) {
				return provider(t, "homerslab-evil2-ldap", "dc=evil2,dc=homerslab", nil)
			}, false},
			{"the tree above every app's, opened to the kids", func(t *testing.T) (int, string) {
				return provider(t, "homerslab-evil3-ldap", "dc=homerslab", []map[string]any{{"group": kids, "enabled": true, "timeout": 30}})
			}, false},
			{"the real app opened to one kid more", func(t *testing.T) (int, string) {
				binding(t, map[string]any{"user": w.userPK(t, "kiko"), "enabled": true, "timeout": 30})
				return 0, ""
			}, false},
			{"the real app opened to everyone but the kids", func(t *testing.T) (int, string) {
				binding(t, map[string]any{"group": kids, "negate": true, "enabled": true, "timeout": 30})
				return 0, ""
			}, false},
			{"the real app's provider on another app's bind flow, which takes that app's app passwords", func(t *testing.T) (int, string) {
				w.raw(t, w.own, "PATCH", fmt.Sprintf("/api/v3/providers/ldap/%d/", realP.Results[0].PK),
					map[string]any{"authorization_flow": flowPK(t, w, authentik.LDAPBindFlow(other.Slug))}, nil)
				return 0, ""
			}, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				pk, evil := c.drift(t)
				if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
					t.Errorf("drift at the outpost: %+v %v, want not passkey-only", r, err)
				}
				if err := idptest.Prepare(ctx, a, household); err != nil {
					t.Fatal(err)
				}
				_, ps := outpost()
				if pk != 0 && slices.Contains(ps, pk) {
					t.Error("the console's provider is still at the outpost")
				}
				if c.realStays && !slices.Contains(ps, realP.Results[0].PK) {
					t.Error("the real app's provider left the outpost with the console's copy of its base DN")
				}
				if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
					t.Errorf("after Prepare: %+v %v", r, err)
				}
				for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
					for _, who := range []string{"kiko", "olga"} {
						if bindAt(creds.UserDN, who) == nil || evil != "" && bindAt(evil, who) == nil {
							t.Fatalf("%s bound, outside the audience, after Prepare", who)
						}
					}
					if w.Bind(t, fmt.Sprintf(creds.UserDN, "lena"), lenaOther) == nil {
						t.Fatal("lena bound at the real app with the other app's app password, after Prepare")
					}
				}
				// The real app as it should be, with its next EnsureLDAP (Prepare closed it, if its own was changed).
				if _, err := a.EnsureLDAP(ctx, real); err != nil {
					t.Fatal(err)
				}
				waitFor(creds.UserDN)
				for _, who := range []string{"kiko", "olga"} {
					if bindAt(creds.UserDN, who) == nil {
						t.Errorf("%s bound, outside the audience, with the real app back", who)
					}
				}
				if w.Bind(t, fmt.Sprintf(creds.UserDN, "lena"), lenaOther) == nil {
					t.Error("lena bound at the real app with the other app's app password, with the real app back")
				}
				// Authentik may be busy a moment on a loaded machine (a worker recycled answers 503): the report once
				// it is ready again (#245).
				w.ready(t)
				if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
					t.Errorf("with the real app back: %+v %v", r, err)
				}
			})
		}
		// An install's EnsureLDAP tidies the outpost itself, not only the hourly pass or Prepare (#246's review): a
		// provider of the console's there goes with the next app's EnsureLDAP alone.
		t.Run("an app's EnsureLDAP alone takes a provider of the console's off the outpost", func(t *testing.T) {
			pk, _ := provider(t, "homerslab-evil4-ldap", "dc=evil4,dc=homerslab", nil)
			if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
				t.Errorf("a provider of the console's at the outpost: %+v %v, want not passkey-only", r, err)
			}
			if _, err := a.EnsureLDAP(ctx, other); err != nil {
				t.Fatal(err)
			}
			if _, ps := outpost(); slices.Contains(ps, pk) {
				t.Error("the console's provider is still at the outpost after an EnsureLDAP")
			}
			if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
				t.Errorf("after an EnsureLDAP: %+v %v", r, err)
			}
		})
		// The outpost's own application, opened to the adults: only the policy that refuses everyone may open it.
		t.Run("the outpost's own application opened to the adults", func(t *testing.T) {
			var app struct {
				PBM string `json:"pbm_uuid"`
			}
			w.raw(t, w.own, "GET", "/api/v3/core/applications/homerslab-outpost-ldap/", nil, &app)
			w.raw(t, w.own, "POST", "/api/v3/policies/bindings/", map[string]any{"target": app.PBM, "order": 100,
				"group": groupPK(idp.RoleGroup(idp.Adult)), "enabled": true, "timeout": 30}, nil)
			if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
				t.Errorf("the outpost's own application opened: %+v %v, want not passkey-only", r, err)
			}
			if err := idptest.Prepare(ctx, a, household); err != nil {
				t.Fatal(err)
			}
			if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
				t.Errorf("after Prepare: %+v %v", r, err)
			}
			for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
				if bindAt("cn=%s,ou=users,ou=outpost,dc=homerslab", "lena") == nil {
					t.Fatal("lena bound at the outpost's own base DN after Prepare")
				}
			}
		})
		if err := a.DeleteLDAP(ctx, real.Slug); err != nil {
			t.Fatal(err)
		}
	}
}

// unitCommand is a packaged unit's own compose command, on the test's project and file, ready to start attached.
func unitCommand(t *testing.T, w *world, name string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	u, err := os.ReadFile(filepath.Join(filepath.Dir(self), "..", "..", "..", "packaging", "systemd", name))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, l := range strings.Split(string(u), "\n") {
		if rest, ok := strings.CutPrefix(l, "ExecStart=/usr/bin/docker compose "); ok {
			args = strings.Fields(rest)
		}
	}
	if len(args) == 0 {
		t.Fatalf("no compose command in %s", name)
	}
	for i := range args {
		switch args[i] {
		case "homerslab-authentik":
			args[i] = project
		case "/usr/share/homeward/sign-in/authentik.compose.yaml":
			args[i] = filepath.Join(w.dir, "compose.yaml")
		}
	}
	c := exec.Command("docker", append([]string{"compose"}, args...)...)
	c.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
	var out strings.Builder
	c.Stdout, c.Stderr = &out, &out
	return c, &out
}

// ldapOwnUnit runs the sign-in's unit's command and the outpost's, attached to the running containers, then stops the
// outpost: its own command ends, so systemd would start it again, and the sign-in's goes on. Last of the LDAP checks:
// the outpost stays stopped, and the sign-in is started again after.
func ldapOwnUnit(w *world) func(*testing.T) {
	return func(t *testing.T) {
		// started runs a command; its channel is closed when it ends, with its error in *err.
		started := func(c *exec.Cmd, err *error) chan struct{} {
			if e := c.Start(); e != nil {
				t.Fatal(e)
			}
			ended := make(chan struct{})
			go func() { *err = c.Wait(); close(ended) }()
			return ended
		}
		signIn, signInOut := unitCommand(t, w, "homeward-authentik.service")
		outpost, outpostOut := unitCommand(t, w, "homeward-ldap.service")
		var signInErr, outpostErr error
		signInEnded, outpostEnded := started(signIn, &signInErr), started(outpost, &outpostErr)
		defer func() {
			// The commands stopped, and the sign-in up again for the checks after these: a compose client that goes
			// away may stop its containers on the way.
			_ = signIn.Process.Kill()
			_ = outpost.Process.Kill()
			<-signInEnded
			<-outpostEnded
			w.compose(t, "up", "-d", "--quiet-pull")
			w.ready(t)
		}()
		time.Sleep(15 * time.Second) // attached
		select {
		case <-outpostEnded:
			t.Fatalf("the outpost's command ended by itself: %v\n%s", outpostErr, outpostOut)
		case <-signInEnded:
			t.Fatalf("the sign-in's command ended by itself: %v\n%s", signInErr, signInOut)
		default:
		}
		w.compose(t, "kill", "ldap")
		select {
		case <-outpostEnded:
		case <-time.After(60 * time.Second):
			t.Error("the outpost stopped, and its unit's command still runs: systemd would never start it again")
		}
		select {
		case <-signInEnded:
			t.Errorf("the outpost stopped, and the sign-in's command ended with it: %v\n%s", signInErr, signInOut)
		case <-time.After(20 * time.Second):
		}
		if code := w.status(t, w.own, "/api/v3/core/users/me/"); code != http.StatusOK {
			t.Errorf("Authentik, after the outpost stopped: %d", code)
		}
	}
}
