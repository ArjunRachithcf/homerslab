package agent_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// outpostToken stands in for the LDAP outpost's token, which the manager hands the agent inward only.
const outpostToken = "OUTPOST-TOKEN-PLACEHOLDER-0123456789abcdefghijklmnop"

// withLDAPS is the machine's users, the LDAPS port's among them.
func withLDAPS(name string) (uint32, bool) {
	uid, ok := map[string]uint32{"homerslab-edge": 981, "homerslab": 980, "homerslab-home": 982, "homerslab-ldaps": 60604}[name]
	return uid, ok
}

// books is an app that signs people in over LDAP (#204), as its install plan hands it to the agent: BentoPDF's compose
// file stands in for its own, with its directory at the sign-in.
func books(t *testing.T) agent.AppRequest {
	t.Helper()
	r := bentoPDF(t)
	r.Plan, r.Product, r.Name, r.Guard = "p-0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", "Books", "books.example.test", false
	r.LDAP = &agent.AppLDAP{Env: catalog.LDAPEnv{URL: "LDAP_URL", Host: "LDAP_HOST", Port: "LDAP_PORT", BaseDN: "LDAP_BASE_DN",
		UserDN: "LDAP_USER_DN", AdminGroup: "LDAP_ADMIN_GROUP"},
		BaseDN: "dc=books,dc=homerslab", UserDN: "cn=%s,ou=users,dc=books,dc=homerslab", RefusePaths: []string{"/api/local-login"}}
	return r
}

// sealedAt is what a credential the test machine sealed holds.
func sealedAt(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(b), "sealed:"))
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}

// lastRan is the index of the last command line that is line or starts with it, -1 if none.
func lastRan(h *agenttest.Host, line string) int {
	at := -1
	for i, c := range h.Cmds() {
		if c.String() == line || strings.HasPrefix(c.String(), line+" ") {
			at = i
		}
	}
	return at
}

// An app that signs people in over LDAP binds at auth.<main domain>:636 from inside its container (#204, ADR 0020):
// its upstream's environment names the LDAPS port, its own base DN and the DN template a person binds with; the LDAPS
// port gets the sign-in's name and certificate, sealed for its unit, and its socket is enabled; the firewall table
// lets that app's network, and no other app's, reach 636. Before an LDAP app, none of it is there.
func TestAnLDAPAppBindsAtTheSignInsLDAPSPortFromItsOwnNetworkAlone(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{Users: withLDAPS})
	dropIn := filepath.Join(h.Root, "etc/systemd/system/homerslab-ldaps.service.d/50-homerslab-certificates.conf")
	install(t, sock, "bentopdf", bentoPDF(t))
	if _, err := os.Stat(dropIn); err == nil || h.Ran("systemctl enable --now homerslab-ldaps.socket") >= 0 {
		t.Error("the LDAPS port, before any app signs in over LDAP")
	}
	if table, _ := h.Table(); strings.Contains(table, ". 636 }") {
		t.Errorf("636 open before any LDAP app:\n%s", table)
	}

	s := install(t, sock, "books", books(t))
	if s.Upstream != "172.31.240.18:8080" || len(s.Pending) != 0 {
		t.Fatalf("the LDAP app: %+v", s)
	}
	dir := filepath.Join(state, "apps", "books")
	o, _ := os.ReadFile(filepath.Join(dir, "homerslab.yaml"))
	for k, v := range map[string]string{"LDAP_URL": "ldaps://auth.example.test:636", "LDAP_HOST": "auth.example.test", "LDAP_PORT": "636",
		"LDAP_BASE_DN": "dc=books,dc=homerslab", "LDAP_USER_DN": "cn=%s,ou=users,dc=books,dc=homerslab",
		"LDAP_ADMIN_GROUP": "cn=homerslab-admins,ou=groups,dc=books,dc=homerslab"} {
		if want := "      \"" + k + "\": \"" + v + "\"\n"; !strings.Contains(string(o), want) {
			t.Errorf("the app's environment lacks %q:\n%s", want, o)
		}
	}
	if !strings.Contains(string(o), "extra_hosts:\n      - \"auth.example.test=172.31.255.241\"\n") || strings.Contains(string(o), "secrets:") {
		t.Errorf("the app's compose file: the sign-in at its listener, no secret:\n%s", o)
	}

	// The LDAPS port: the sign-in's name, and its certificate sealed again under the unit's own names.
	d, err := os.ReadFile(dropIn)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment=HOMERSLAB_SIGN_IN=auth.example.test\n",
		"LoadCredentialEncrypted=tls.crt:" + filepath.Join(state, "ldaps", "tls.crt.cred") + "\n",
		"LoadCredentialEncrypted=tls.key:" + filepath.Join(state, "ldaps", "tls.key.cred") + "\n"} {
		if !strings.Contains(string(d), want) {
			t.Errorf("the LDAPS port's drop-in lacks %q:\n%s", want, d)
		}
	}
	for _, id := range []string{"crt", "key"} {
		if sealedAt(t, filepath.Join(state, "ldaps", "tls."+id+".cred")) != sealedAt(t, filepath.Join(state, "edge", "tls-example.test."+id+".cred")) {
			t.Errorf("the LDAPS port's tls.%s isn't the sign-in's", id)
		}
	}
	for _, c := range h.Cmds() {
		if c.Name == "systemd-creds" && c.Args[0] == "encrypt" && strings.Contains(c.String(), "/ldaps/") &&
			!strings.Contains(c.String(), "--name=tls.crt ") && !strings.Contains(c.String(), "--name=tls.key ") {
			t.Errorf("sealed for the LDAPS port under another name: %s", c)
		}
	}
	if reload, enable := lastRan(h, "systemctl daemon-reload"), h.Ran("systemctl enable --now homerslab-ldaps.socket"); enable < 0 || reload > enable {
		t.Errorf("reload %d, socket enabled %d", reload, enable)
	}

	// Only the LDAP app's network reaches 636, by its bridge and its subnet together.
	table, _ := h.Table()
	want := "\t\tiifname \"" + appunit.Bridge(1, -1) + "\" ip saddr 172.31.240.16/28 ip daddr . tcp dport { 172.31.255.241 . 636 } accept\n"
	if !strings.Contains(table, want) || strings.Count(table, ". 636 }") != 1 {
		t.Errorf("the table lacks %q, or opens 636 to another:\n%s", want, table)
	}

	// The edge serves it as an app that signs people in itself: no guard, its own way in refused.
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct{ Routes []json.RawMessage }
			}
		}
	}
	_ = json.Unmarshal(h.Edge.Config(), &cfg)
	site := ""
	for _, r := range cfg.Apps.HTTP.Servers["mesh"].Routes {
		if compact := strings.Join(strings.Fields(string(r)), ""); strings.Contains(compact, `"host":["books.example.test"]`) {
			site = compact
		}
	}
	if !strings.Contains(site, `"dial":"172.31.240.18:8080"`) || strings.Contains(site, "outpost.goauthentik.io") ||
		!strings.Contains(site, `"path":["/api/local-login","/api/local-login/*"]`) {
		t.Errorf("the LDAP app's site:\n%s", site)
	}

	// Installed again, nothing of the LDAPS port changes: no restart of a port that serves.
	before := h.Times("systemctl --no-block try-restart homerslab-ldaps.service")
	install(t, sock, "books", books(t))
	if h.Times("systemctl --no-block try-restart homerslab-ldaps.service") != before {
		t.Error("the LDAPS port restarted though nothing of it changed")
	}
}

// A renewed certificate reaches the LDAPS port too (#204, part 2b): the agent seals it again for the port's unit, and
// the unit, if it runs, restarts, so it serves the new one, which it reads only as it starts. Without an LDAP app, a
// renewal leaves the LDAPS port alone.
func TestTheLDAPSPortServesTheRenewedCertificate(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{Users: withLDAPS})
	chain, key := wildcard(t, "example.test")
	if code, body := call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key}); code != http.StatusOK {
		t.Fatalf("a renewal: %d %s", code, body)
	}
	if h.Ran("systemctl --no-block try-restart homerslab-ldaps.service") >= 0 {
		t.Error("a renewal restarted the LDAPS port before any LDAP app")
	}
	if _, err := os.Stat(filepath.Join(state, "ldaps")); err == nil {
		t.Error("a renewal sealed the sign-in's key for the LDAPS port before any LDAP app")
	}
	install(t, sock, "books", books(t))
	first := sealedAt(t, filepath.Join(state, "ldaps", "tls.crt.cred"))

	chain, key = wildcard(t, "example.test")
	if code, body := call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key}); code != http.StatusOK {
		t.Fatalf("a renewal: %d %s", code, body)
	}
	got := sealedAt(t, filepath.Join(state, "ldaps", "tls.crt.cred"))
	if got == first || got != sealedAt(t, filepath.Join(state, "edge", "tls-example.test.crt.cred")) {
		t.Error("the LDAPS port's certificate isn't the renewed one")
	}
	if sealedAt(t, filepath.Join(state, "ldaps", "tls.key.cred")) != key {
		t.Error("the LDAPS port's key isn't the renewed one")
	}
	sealed, restarted := lastRan(h, "systemd-creds encrypt --with-key=host+tpm2 --name=tls.key"), lastRan(h, "systemctl --no-block try-restart homerslab-ldaps.service")
	if sealed < 0 || restarted < sealed {
		t.Errorf("the key sealed at %d, the LDAPS port restarted at %d: it would serve the old certificate", sealed, restarted)
	}
}

// The LDAP outpost's token reaches its own unit only (#204): the agent seals it, as root, under its own name, and
// writes the outpost unit's drop-in that copies it for the outpost's container user, labelled at the sign-in's level;
// systemd enables and starts the unit. The same token again restarts nothing; a new one restarts the outpost, which
// reads its token only as it starts. The token is never on a command line, in an answer or a file in the clear.
func TestTheLDAPOutpostGetsItsTokenSealedForItsOwnUnit(t *testing.T) {
	testenv.Isolate(t)
	{
		sock, _, _ := joined(t, agent.Config{})
		if code, _ := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken}); code != http.StatusConflict {
			t.Errorf("the outpost's token before the sign-in: %d, want 409", code)
		}
	}
	sock, h, state := signedIn(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}})
	code, body := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken})
	if code != http.StatusNoContent {
		t.Fatalf("the outpost's token: %d %s", code, body)
	}
	cred := filepath.Join(state, "sign-in", "authentik.ldap-token.cred")
	if sealedAt(t, cred) != outpostToken {
		t.Error("the sealed token isn't the manager's")
	}
	if h.Ran("systemd-creds encrypt --with-key=host+tpm2 --name=authentik.ldap-token - "+cred+".new") < 0 {
		t.Error("the token wasn't sealed under its own name")
	}
	b, err := os.ReadFile(filepath.Join(h.Root, "etc/systemd/system/homerslab-ldap.service.d/50-homerslab-credentials.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LoadCredentialEncrypted=authentik.ldap-token:" + cred + "\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 %d/authentik.ldap-token /run/homerslab-ldap/ldap-token\n",
		"ExecStartPre=/usr/bin/chcon -t container_file_t -l s0:c1000,c1001 /run/homerslab-ldap/ldap-token\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the outpost's drop-in lacks %q:\n%s", want, b)
		}
	}
	reload, enable, restart := lastRan(h, "systemctl daemon-reload"), h.Ran("systemctl enable homerslab-ldap.service"), h.Ran("systemctl --no-block restart homerslab-ldap.service")
	if enable < 0 || restart < enable || reload > enable {
		t.Errorf("reload %d, enable %d, restart %d: the unit gets its token only as it starts", reload, enable, restart)
	}
	restarts := func() int { return h.Times("systemctl --no-block restart homerslab-ldap.service") }
	n := restarts()
	if code, _ := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken}); code != http.StatusNoContent || restarts() != n {
		t.Errorf("the same token again: %d, restarts %d → %d: the outpost would drop its binds for nothing", code, n, restarts())
	}
	if code, _ := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken + "-new"}); code != http.StatusNoContent ||
		restarts() != n+1 || sealedAt(t, cred) != outpostToken+"-new" {
		t.Errorf("a new token: %d, restarts %d → %d", code, n, restarts())
	}
	for _, c := range h.Cmds() {
		if strings.Contains(c.String(), outpostToken) {
			t.Errorf("the token on a command line: %s", c)
		}
	}
	_ = filepath.Walk(state, func(p string, fi os.FileInfo, err error) error {
		if b, _ := os.ReadFile(p); err == nil && !fi.IsDir() && strings.Contains(string(b), outpostToken) {
			t.Errorf("the token in the clear in %s", p)
		}
		return nil
	})
	for name, token := range map[string]string{"empty": "", "two words": "a b c d e f g h i j k", "short": "abc", "a line break": outpostToken + "\nx"} {
		if code, _ := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: token}); code != http.StatusBadRequest {
			t.Errorf("a token %s: %d, want 400", name, code)
		}
	}
}

// The agent takes an LDAP app's directory only as the app's own (#204): its own base DN, never another app's or the
// tree above them all, the DN template in it, its settings' names plain and once; and an app signs in one way.
func TestAnLDAPDirectoryIsTakenOnlyAsTheAppsOwn(t *testing.T) {
	testenv.Isolate(t)
	sock, _, _ := signedIn(t, agent.Config{Users: withLDAPS})
	for name, c := range map[string]struct {
		edit func(*agent.AppRequest)
		want string
	}{
		"another app's base DN": {func(r *agent.AppRequest) {
			r.LDAP.BaseDN, r.LDAP.UserDN = "dc=bentopdf,dc=homerslab", "cn=%s,ou=users,dc=bentopdf,dc=homerslab"
		}, "bad-sign-in"},
		"the tree above every app's": {func(r *agent.AppRequest) {
			r.LDAP.BaseDN, r.LDAP.UserDN = "dc=homerslab", "cn=%s,ou=users,dc=homerslab"
		}, "bad-sign-in"},
		"a DN template elsewhere":               {func(r *agent.AppRequest) { r.LDAP.UserDN = "cn=%s,ou=users,dc=bentopdf,dc=homerslab" }, "bad-sign-in"},
		"a DN template of anyone":               {func(r *agent.AppRequest) { r.LDAP.UserDN = "cn=*,ou=users,dc=books,dc=homerslab" }, "bad-sign-in"},
		"a variable that isn't one":             {func(r *agent.AppRequest) { r.LDAP.Env.URL = "LDAP URL" }, "bad-sign-in"},
		"one variable twice":                    {func(r *agent.AppRequest) { r.LDAP.Env.Host = "LDAP_URL" }, "bad-sign-in"},
		"no base DN's variable":                 {func(r *agent.AppRequest) { r.LDAP.Env.BaseDN = "" }, "bad-sign-in"},
		"a refused route of every path":         {func(r *agent.AppRequest) { r.LDAP.RefusePaths = []string{"/"} }, "bad-sign-in"},
		"the guard as well":                     {func(r *agent.AppRequest) { r.Guard = true }, "bad-app"},
		"an OIDC client as well":                {func(r *agent.AppRequest) { r.OIDC = mealie(t).OIDC }, "bad-app"},
		"no way to sign in":                     {func(r *agent.AppRequest) { r.LDAP = nil }, "sign-in-not-built"},
		"an empty directory, when not checking": {func(r *agent.AppRequest) { r.LDAP.BaseDN, r.LDAP.UserDN = "", "" }, "bad-sign-in"},
	} {
		t.Run(name, func(t *testing.T) {
			r := books(t)
			c.edit(&r)
			if code, body := call(t, sock, "PUT", "/v1/apps/books", r); code != http.StatusBadRequest || !strings.Contains(string(body), c.want) {
				t.Errorf("%d %s, want 400 %s", code, body, c.want)
			}
		})
	}
	// Only checking, before the directory is made: no DNs yet.
	r := books(t)
	r.LDAP.BaseDN, r.LDAP.UserDN = "", ""
	if code, body := call(t, sock, "PUT", "/v1/apps/books?check=true", r); code != http.StatusOK {
		t.Errorf("checking before the directory is made: %d %s", code, body)
	}
	// Nowhere to bind before the edge serves the sign-in.
	{
		sock, _, _ := joined(t, agent.Config{})
		if code, body := call(t, sock, "PUT", "/v1/apps/books", books(t)); code != http.StatusBadRequest || !strings.Contains(string(body), "no-sign-in") {
			t.Errorf("an LDAP app before the sign-in: %d %s", code, body)
		}
	}
}

// A step that fails is done again (#204, part 2b): an LDAPS socket that systemd didn't enable is enabled at the next
// check, and an outpost unit that didn't start starts at the manager's next call with the same token, though nothing
// of what the agent wrote changed in between.
func TestAnLDAPStepThatFailedIsDoneAgain(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{Users: withLDAPS, CheckEvery: 50 * time.Millisecond})
	enable := "systemctl enable --now homerslab-ldaps.socket"
	h.Fail(enable)
	if s := install(t, sock, "books", books(t)); !strings.Contains(strings.Join(s.Pending, " "), "ldaps") {
		t.Errorf("the LDAPS port didn't take, and the answer doesn't say so: %+v", s)
	}
	failed := h.Times(enable)
	h.Mend()
	for deadline := time.Now().Add(5 * time.Second); h.Times(enable) == failed; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the watch never enabled the LDAPS socket again")
		}
	}

	start := "systemctl enable homerslab-ldap.service"
	h.Fail(start)
	if code, _ := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken}); code == http.StatusNoContent {
		t.Error("the outpost's unit didn't start, and the answer says it did")
	}
	h.Mend()
	n := h.Times(start)
	if code, body := call(t, sock, "PUT", "/v1/sign-in/ldap", agent.LDAPRequest{Token: outpostToken}); code != http.StatusNoContent || h.Times(start) != n+1 ||
		h.Times("systemctl --no-block restart homerslab-ldap.service") != 1 {
		t.Errorf("the same token after a failed start: %d %s, enabled %d → %d", code, body, n, h.Times(start))
	}
}
