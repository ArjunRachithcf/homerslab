package authentik_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
	"github.com/ArjunRachithcf/homerslab/internal/ldaps/ldaptest"
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

// AppPassword is an app password of the person's, made as Home will make one for them: never shown again after.
func (w *world) AppPassword(t *testing.T, username string) string {
	t.Helper()
	id := "hltest-app-password-" + username
	w.raw(t, w.own, "POST", "/api/v3/core/tokens/", map[string]any{"identifier": id, "intent": "app_password",
		"user": w.userPK(t, username), "expiring": false}, nil)
	var k struct {
		Key string `json:"key"`
	}
	w.raw(t, w.own, "GET", "/api/v3/core/tokens/"+id+"/view_key/", nil, &k)
	if k.Key == "" {
		t.Fatalf("no app password for %s", username)
	}
	return k.Key
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

// ldapFlowOnlyAtTheOutpost checks that a browser can't run the bind flow: an app password signs nobody in to the
// provider itself.
func ldapFlowOnlyAtTheOutpost(t *testing.T) {
	req, _ := http.NewRequest("GET", upstream+"/api/v3/flows/executor/"+authentik.LDAPBindFlow+"/?query=", nil)
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

// ldapDrift opens the bind flow to passwords behind HomersLab's back, as an admin in Authentik's console could: the
// report says the sign-in is no longer passkey-only, and Prepare puts the flow back.
func ldapDrift(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
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
		w.raw(t, w.own, "PATCH", "/api/v3/flows/instances/"+authentik.LDAPBindFlow+"/", map[string]any{"authentication": "none"}, nil)
		if err := a.Prepare(ctx, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare: %+v %v", r, err)
		}
		ldapFlowOnlyAtTheOutpost(t)
		// The app password's stage taken out of the flow: a username alone would bind.
		var bs struct {
			Results []struct {
				PK    string `json:"pk"`
				Stage string `json:"stage"`
			}
		}
		w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+flowPK(t, w), nil, &bs)
		for _, b := range bs.Results {
			if b.Stage == st.Results[0].PK {
				w.raw(t, w.own, "DELETE", "/api/v3/flows/bindings/"+b.PK+"/", nil, nil)
			}
		}
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow without its password stage: %+v %v, want not passkey-only", r, err)
		}
		if err := a.Prepare(ctx, household); err != nil {
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
		w.raw(t, w.own, "GET", "/api/v3/flows/bindings/?target="+flowPK(t, w), nil, &fsb)
		for _, b := range fsb.Results {
			if b.Stage == st.Results[0].PK && len(never.Results) == 1 {
				w.raw(t, w.own, "POST", "/api/v3/policies/bindings/", map[string]any{"target": b.PBM, "policy": never.Results[0].PK, "order": 0}, nil)
			}
		}
		if r, err := a.Report(ctx); err != nil || r.PasskeyOnlySignIn {
			t.Errorf("a bind flow with a policy on its password stage: %+v %v, want not passkey-only", r, err)
		}
		if err := a.Prepare(ctx, household); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Report(ctx); err != nil || !r.PasskeyOnlySignIn {
			t.Errorf("after Prepare, a third time: %+v %v", r, err)
		}
	}
}

func flowPK(t *testing.T, w *world) string {
	var f struct {
		PK string `json:"pk"`
	}
	w.raw(t, w.own, "GET", "/api/v3/flows/instances/"+authentik.LDAPBindFlow+"/", nil, &f)
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
