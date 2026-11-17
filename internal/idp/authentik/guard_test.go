package authentik_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/idp/authentik"
	"github.com/ArjunRachithcf/homeward/internal/idp/idptest"
)

// scopeMapping is a scope mapping as the suite reads it behind the adapter's back.
type scopeMapping struct {
	PK         string  `json:"pk"`
	Name       string  `json:"name"`
	Managed    *string `json:"managed"`
	Scope      string  `json:"scope_name"`
	Expression string  `json:"expression"`
}

// guardProvider is a guard's proxy provider as the suite reads it.
type guardProvider struct {
	PK       int      `json:"pk"`
	Name     string   `json:"name"`
	Mappings []string `json:"property_mappings"`
}

func (w *world) roleMapping(t *testing.T) scopeMapping {
	t.Helper()
	var ms struct{ Results []scopeMapping }
	w.raw(t, w.own, "GET", "/api/v3/propertymappings/provider/scope/?page_size=500", nil, &ms)
	var mine []scopeMapping
	for _, m := range ms.Results {
		if m.Name == authentik.RoleMapping {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("role mappings: %+v, want one", mine)
	}
	return mine[0]
}

func (w *world) guardProvider(t *testing.T, slug string) guardProvider {
	t.Helper()
	var ps struct{ Results []guardProvider }
	w.raw(t, w.own, "GET", "/api/v3/providers/proxy/?name__iexact="+slug, nil, &ps)
	if len(ps.Results) != 1 || ps.Results[0].Name != slug {
		t.Fatalf("the provider %s: %+v", slug, ps.Results)
	}
	return ps.Results[0]
}

// evaluate runs the role mapping for a person through Authentik's own test of a mapping, which evaluates it as a
// token for them would, and returns the headers its answer gives the outpost. It never goes through the outpost,
// so the order in which the outpost writes headers can't decide what it shows.
func (w *world) evaluate(t *testing.T, username string) map[string]string {
	t.Helper()
	var res struct {
		Successful bool   `json:"successful"`
		Result     string `json:"result"`
	}
	w.raw(t, w.own, "POST", "/api/v3/propertymappings/all/"+w.roleMapping(t).PK+"/test/", map[string]any{"user": w.userPK(t, username)}, &res)
	var out struct {
		Proxy struct {
			Attrs struct {
				Headers map[string]string `json:"additionalHeaders"`
			} `json:"user_attributes"`
		} `json:"ak_proxy"`
	}
	if !res.Successful || json.Unmarshal([]byte(res.Result), &out) != nil {
		t.Fatalf("the role mapping for %s: %+v", username, res)
	}
	return out.Proxy.Attrs.Headers
}

// roleMappingFailsClosed (#244's nit): with attributes that would set a header of Authentik's or Homeward's under
// another spelling, the mapping names no role at all, whichever spelling the outpost would write last, and empties
// each of them. Theo is a kid throughout, so a mapping that only emptied the forged keys would still name "kid".
func roleMappingFailsClosed(w *world) func(*testing.T) {
	return func(t *testing.T) {
		theo := w.userPK(t, "theo")
		if h := w.evaluate(t, "theo"); h[authentik.RoleHeader] != string(idp.Kid) {
			t.Fatalf("theo, with no attributes: %v, want a kid", h)
		}
		forged := map[string]any{"x-authentik-homerslab-role": "admin", "X-AUTHENTIK-HOMERSLAB-ROLE": "admin",
			"X-Authentik-Uid": "forged-uid", "x_authentik_name": "forged", "X-HomersLab-Role": "admin"}
		check := func(how string) {
			t.Helper()
			h := w.evaluate(t, "theo")
			if role, ok := h[authentik.RoleHeader]; !ok || role != "" {
				t.Errorf("%s: the role header %q (there: %v), want there and empty", how, role, ok)
			}
			for k := range forged {
				if v, ok := h[k]; !ok || v != "" {
					t.Errorf("%s: %s is %q (there: %v), want emptied", how, k, v, ok)
				}
			}
		}
		w.raw(t, w.own, "PATCH", fmt.Sprintf("/api/v3/core/users/%d/", theo), map[string]any{"attributes": map[string]any{"additionalHeaders": forged}}, nil)
		check("in the person's attributes")
		w.raw(t, w.own, "PATCH", fmt.Sprintf("/api/v3/core/users/%d/", theo), map[string]any{"attributes": map[string]any{}}, nil)
		var house struct {
			PK string `json:"pk"`
		}
		w.raw(t, w.own, "POST", "/api/v3/core/groups/", map[string]any{"name": "the-house-244", "users": []int{theo},
			"attributes": map[string]any{"additionalHeaders": forged}}, &house)
		check("in a group's attributes")
		w.raw(t, w.own, "DELETE", "/api/v3/core/groups/"+house.PK+"/", nil, nil)
		if h := w.evaluate(t, "theo"); h[authentik.RoleHeader] != string(idp.Kid) {
			t.Errorf("theo, with the attributes gone: %v, want a kid again", h)
		}
	}
}

// guardDrift (#244): a scope mapping on a guard's provider other than Authentik's own and Homeward's, which could name
// the role after Homeward's does, or Homeward's role mapping changed, shows in the report (ForwardAuth false), and
// Prepare puts the guard back.
func guardDrift(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx, slug := context.Background(), "homerslab-chores"
		intact := func(how string, want bool) {
			t.Helper()
			r, err := a.Report(ctx)
			if err != nil || r.ForwardAuth != want {
				t.Errorf("%s: the report %+v %v, want ForwardAuth %v", how, r, err, want)
				var ps, ms struct{ Results []map[string]any }
				w.raw(t, w.own, "GET", "/api/v3/providers/proxy/?page_size=500", nil, &ps)
				w.raw(t, w.own, "GET", "/api/v3/propertymappings/provider/scope/?page_size=500", nil, &ms)
				for _, p := range ps.Results {
					t.Logf("provider %v of %v: %v", p["name"], p["assigned_application_slug"], p["property_mappings"])
				}
				for _, m := range ms.Results {
					t.Logf("mapping %v %q managed %v scope %v", m["pk"], m["name"], m["managed"], m["scope_name"])
				}
				t.Logf("the role mapping's expression: %q", w.roleMapping(t).Expression)
			}
		}
		prepare := func() {
			t.Helper()
			if err := idptest.Prepare(ctx, a, household); err != nil {
				t.Fatal(err)
			}
		}
		intact("as Homeward made it", true)
		before := w.guardProvider(t, slug).Mappings
		mine := w.roleMapping(t)

		// An admin's mapping that sorts after Homeward's, naming the role admin.
		var zzz scopeMapping
		w.raw(t, w.own, "POST", "/api/v3/propertymappings/provider/scope/", map[string]any{"name": "zzz", "scope_name": "zzz",
			"expression": fmt.Sprintf("return {\"ak_proxy\": {\"user_attributes\": {\"additionalHeaders\": {%q: \"admin\"}}}}", authentik.RoleHeader)}, &zzz)
		defer w.raw(t, w.own, "DELETE", "/api/v3/propertymappings/provider/scope/"+zzz.PK+"/", nil, nil)
		p := w.guardProvider(t, slug)
		w.setMappings(t, p.PK, append(p.Mappings, zzz.PK))
		if !slices.Contains(w.guardProvider(t, slug).Mappings, zzz.PK) {
			t.Fatal("the foreign mapping didn't stay on the provider")
		}
		intact("a foreign mapping on a guard", false)
		prepare()
		if got := w.guardProvider(t, slug).Mappings; slices.Contains(got, zzz.PK) || !sameSet(got, before) {
			t.Errorf("after Prepare, the provider carries %v, want %v", got, before)
		}
		intact("after Prepare", true)

		// A foreign mapping in the role mapping's own scope, whose order beside it Authentik doesn't fix.
		w.raw(t, w.own, "PATCH", "/api/v3/propertymappings/provider/scope/"+zzz.PK+"/", map[string]any{"scope_name": "homerslab"}, nil)
		w.setMappings(t, p.PK, append(before, zzz.PK))
		intact("a foreign mapping in Homeward's scope", false)
		prepare()
		intact("after Prepare", true)

		// Homeward's role mapping taken off the guard: the edge would refuse everyone, and the report says so.
		var without []string
		for _, m := range before {
			if m != mine.PK {
				without = append(without, m)
			}
		}
		w.setMappings(t, p.PK, without)
		intact("the role mapping off a guard", false)
		prepare()
		intact("after Prepare", true)
		if got := w.guardProvider(t, slug).Mappings; !sameSet(got, before) {
			t.Errorf("after Prepare, the provider carries %v, want %v", got, before)
		}

		// The guard switched to the whole domain (#83: never forward_domain), its mappings left alone.
		w.raw(t, w.own, "PATCH", fmt.Sprintf("/api/v3/providers/proxy/%d/", p.PK), map[string]any{"mode": "forward_domain",
			"cookie_domain": household.Domain}, nil)
		intact("a guard for the whole domain", false)
		prepare()
		intact("after Prepare", true)
		var mode struct {
			Mode   string `json:"mode"`
			Cookie string `json:"cookie_domain"`
		}
		w.raw(t, w.own, "GET", fmt.Sprintf("/api/v3/providers/proxy/%d/", p.PK), nil, &mode)
		if mode.Mode != "forward_single" || mode.Cookie != "" {
			t.Errorf("after Prepare, the guard is %+v, want forward_single with no cookie domain", mode)
		}

		// Homeward's role mapping changed: its expression, then its scope (one before ak_proxy's would lose to the
		// person's attributes).
		for _, change := range []map[string]any{
			{"expression": strings.Replace(mine.Expression, `and not forged`, ``, 1)},
			{"expression": fmt.Sprintf("return {\"ak_proxy\": {\"user_attributes\": {\"additionalHeaders\": {%q: \"admin\"}}}}", authentik.RoleHeader)},
			{"scope_name": "aaa"},
		} {
			w.raw(t, w.own, "PATCH", "/api/v3/propertymappings/provider/scope/"+mine.PK+"/", change, nil)
			intact(fmt.Sprintf("the role mapping changed (%v)", change), false)
			prepare()
			intact("after Prepare", true)
			if got := w.roleMapping(t); got.Expression != mine.Expression || got.Scope != mine.Scope {
				t.Errorf("after Prepare, the role mapping is %+v, want %+v", got, mine)
			}
		}
	}
}

// setMappings puts a guard's provider's scope mappings, as an admin in the console could. Authentik checks a proxy
// provider's mode on each change, so the change names it too.
func (w *world) setMappings(t *testing.T, pk int, mappings []string) {
	t.Helper()
	w.raw(t, w.own, "PATCH", fmt.Sprintf("/api/v3/providers/proxy/%d/", pk), map[string]any{"mode": "forward_single", "property_mappings": mappings}, nil)
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
