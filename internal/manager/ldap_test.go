package manager_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// ldapCatalog is the official catalog with BentoPDF signing people in over LDAP instead of behind the guard (#204):
// it stands in for an LDAP app, which the official catalog doesn't have yet.
func ldapCatalog(t *testing.T) fs.FS {
	t.Helper()
	return withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		out := bytes.Replace(b, []byte("signIn:\n  - method: guard\n"), []byte("signIn:\n  - method: ldap\n"+
			"    ldap: {url: LDAP_URL, baseDn: LDAP_BASE_DN, userDn: LDAP_USER_DN, adminGroup: LDAP_ADMIN_GROUP}\n"+
			"    refusePaths: [/api/local-login]\n"), 1)
		if bytes.Equal(out, b) {
			t.Fatal("BentoPDF's manifest has no guard to replace")
		}
		return out
	}})
}

// ldaps are the apps' LDAP directories the household account was asked for, in order.
func ldaps(fs *fakeSignIn) []idp.LDAPApp {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]idp.LDAPApp(nil), fs.ldaps...)
}

// An app that signs people in over LDAP (#204): its plan is wired; applying it has the household account make the
// outpost (its token sealed by the agent for the outpost's own unit) and the app's own directory, for the plan's
// audience, and hands the agent the app's base DN and DN template, which the app takes from its environment. No guard
// is made for it, and the outpost's token is nowhere but sealed.
func TestApplyingAnLDAPPlanMakesTheAppsOwnDirectory(t *testing.T) {
	testenv.Isolate(t)
	var logs lockedBuffer
	p, fs := installable(t, ldapCatalog(t), withLogs(&logs))
	made := planFor(t, p, "bentopdf")
	var shown struct {
		SignIn struct {
			Method, Creates string
			Wired           bool
		}
	}
	p.control("GET", "/api/v1/plans/"+made.ID, nil).json(t, &shown)
	if shown.SignIn.Method != "ldap" || shown.SignIn.Creates != "ldap-access" || !shown.SignIn.Wired {
		t.Errorf("the plan's sign-in: %+v, want LDAP, wired", shown.SignIn)
	}
	if len(ldaps(fs)) != 0 {
		t.Fatal("a plan made a directory before it was applied")
	}
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	want := []idp.LDAPApp{{Slug: "homerslab-bentopdf", Name: "BentoPDF", Roles: []idp.Role{idp.Admin, idp.Adult}}}
	if !reflect.DeepEqual(ldaps(fs), want) || len(fs.guards) != 0 || len(appClients(fs)) != 0 {
		t.Errorf("the directories: %+v, want %+v; guards %+v, clients %+v, want none", ldaps(fs), want, fs.guards, appClients(fs))
	}
	agentDir := filepath.Dir(p.host.Root)
	cred, err := os.ReadFile(filepath.Join(agentDir, "state", "sign-in", "authentik.ldap-token.cred"))
	if err != nil || string(cred) != "sealed:"+base64.StdEncoding.EncodeToString([]byte(outpostToken)) {
		t.Errorf("the outpost's token, sealed by the agent: %q %v", cred, err)
	}
	override, err := os.ReadFile(filepath.Join(agentDir, "state", "apps", "bentopdf", "homeward.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"LDAP_URL": "ldaps://auth.example.test:636"`, `"LDAP_BASE_DN": "dc=bentopdf,dc=homerslab"`,
		`"LDAP_USER_DN": "cn=%s,ou=users,dc=bentopdf,dc=homerslab"`, `"LDAP_ADMIN_GROUP": "cn=homerslab-admins,ou=groups,dc=bentopdf,dc=homerslab"`} {
		if !strings.Contains(string(override), want) {
			t.Errorf("the app's settings lack %s:\n%s", want, override)
		}
	}
	if cfg := strings.Join(strings.Fields(string(p.host.Edge.Config())), ""); !strings.Contains(cfg, `"path":["/api/local-login","/api/local-login/*"]`) {
		t.Errorf("the edge doesn't refuse the app's own sign-in:\n%s", cfg)
	}
	db, _ := os.ReadFile(p.db)
	wal, _ := os.ReadFile(p.db + "-wal")
	for where, b := range map[string]string{"the database": string(db) + string(wal), "the logs": logs.String()} {
		if strings.Contains(b, outpostToken) {
			t.Errorf("%s hold the outpost's token", where)
		}
	}
}

// Every pass of the manager's schedule puts each LDAP app's directory back as its plan says (#204): its audience, and
// whatever the outpost's tidying took off. Apps that sign in otherwise get none.
func TestEveryPassPutsEachLDAPAppsDirectoryBack(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, ldapCatalog(t))
	for _, app := range []string{"bentopdf", "mealie"} {
		if r := p.control("POST", "/api/v1/plans/"+planFor(t, p, app).ID+"/apply", nil); r.Code != http.StatusAccepted {
			t.Fatalf("applying %s: %d %s", app, r.Code, r.Body)
		}
	}
	before, outposts := len(ldaps(fs)), fs.outposts
	p.m.RunDue(context.Background())
	got := ldaps(fs)
	want := idp.LDAPApp{Slug: "homerslab-bentopdf", Name: "BentoPDF", Roles: []idp.Role{idp.Admin, idp.Adult}}
	if len(got) != before+1 || !reflect.DeepEqual(got[len(got)-1], want) {
		t.Errorf("after a pass, the directories asked for: %+v, want one more, %+v", got, want)
	}
	if fs.outposts != outposts+1 {
		t.Errorf("after a pass, the outpost asked for %d times, want %d: its token back with the agent", fs.outposts, outposts+1)
	}
	// The household account failing doesn't stop the pass: the outpost's token still goes back to the agent, and the
	// next pass, the account back, puts the directory back.
	fs.ldapFails = errors.New("the household account is down")
	outposts = fs.outposts
	p.m.RunDue(context.Background())
	if fs.outposts != outposts+1 {
		t.Errorf("a pass with the household account failing asked for the outpost %d times, want %d", fs.outposts, outposts+1)
	}
	fs.mu.Lock()
	fs.ldapFails = nil
	fs.mu.Unlock()
	before = len(ldaps(fs))
	p.m.RunDue(context.Background())
	if got := ldaps(fs); len(got) != before+1 || !reflect.DeepEqual(got[len(got)-1], want) {
		t.Errorf("the pass after the household account came back: %+v, want one more, %+v", got, want)
	}
}

// A pass tidies the outpost once, whatever the number of LDAP apps (#246's review): a tidy reads every provider, so one
// per app would grow as the square of the apps, while the pass holds up every apply.
func TestAPassTidiesTheOutpostOnceForAllItsLDAPApps(t *testing.T) {
	testenv.Isolate(t)
	cat := withCatalog(t, map[string]func([]byte) []byte{
		"apps/bentopdf/app.yaml": func(b []byte) []byte {
			return bytes.Replace(b, []byte("signIn:\n  - method: guard\n"), []byte("signIn:\n  - method: ldap\n"+
				"    ldap: {url: LDAP_URL, baseDn: LDAP_BASE_DN, userDn: LDAP_USER_DN, adminGroup: LDAP_ADMIN_GROUP}\n"), 1)
		},
		"apps/mealie/app.yaml": func(b []byte) []byte {
			out := regexp.MustCompile(`(?s)signIn:\n  - method: oidc\n.*?\n(\S)`).ReplaceAll(b, []byte("signIn:\n  - method: ldap\n"+
				"    ldap: {url: LDAP_URL, baseDn: LDAP_BASE_DN, userDn: LDAP_USER_DN, adminGroup: LDAP_ADMIN_GROUP}\n$1"))
			if bytes.Equal(out, b) {
				t.Fatal("Mealie's manifest has no OIDC sign-in to replace")
			}
			return out
		},
	})
	p, fs := installable(t, cat)
	for _, app := range []string{"bentopdf", "mealie"} {
		if r := p.control("POST", "/api/v1/plans/"+planFor(t, p, app).ID+"/apply", nil); r.Code != http.StatusAccepted {
			t.Fatalf("applying %s: %d %s", app, r.Code, r.Body)
		}
	}
	fs.mu.Lock()
	tidies, before := fs.tidies, len(fs.ldaps)
	fs.mu.Unlock()
	p.m.RunDue(context.Background())
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.ldaps) != before+2 || fs.tidies != tidies+1 {
		t.Errorf("a pass with two LDAP apps: %d directories put back, %d tidies of the outpost; want 2 and 1", len(fs.ldaps)-before, fs.tidies-tidies)
	}
}

// An LDAP app whose install fails after its directory was made leaves no directory behind (#204); a directory that
// can't be made installs nothing.
func TestAnLDAPInstallThatFailsDeletesItsNewDirectory(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, ldapCatalog(t))
	made := planFor(t, p, "bentopdf")
	p.host.Fail("systemctl enable /etc/homeward/units")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code/100 == 2 {
		t.Fatalf("applying with systemd failing: %d %s", r.Code, r.Body)
	}
	if fmt.Sprint(fs.deleted) != "[homerslab-bentopdf-ldap]" {
		t.Errorf("deleted %v: want the directory this apply made", fs.deleted)
	}
	p.host.Mend()
	fs.ldapFails = errors.New("the household account is down")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusBadGateway || !strings.Contains(string(r.Body), "ldap-failed") {
		t.Errorf("with the household account failing: %d %s", r.Code, r.Body)
	}
	if len(agentApps(t, p).Apps) != 0 {
		t.Error("an app was installed without its directory")
	}
}
