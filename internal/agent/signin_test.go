package agent_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// withSELinux is a sysroot whose kernel enforces SELinux, over another.
type withSELinux struct{ fs.FS }

func (s withSELinux) Open(name string) (fs.File, error) {
	if name == "sys/fs/selinux/enforce" {
		return fstest.MapFS{name: {Data: []byte("1")}}.Open(name)
	}
	return s.FS.Open(name)
}

const bootstrap = "bootstrap-PLACEHOLDER-0123456789abcdef"

func signInDropIn(t *testing.T, h *agenttest.Host) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.Root, "etc/systemd/system/homerslab-authentik.service.d/50-homerslab-credentials.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The sign-in is one systemd unit running the standard compose command (the package's homerslab-authentik.service).
// The agent makes Authentik's secrets itself and seals them, as root, for that unit: the unit decrypts them as it
// starts, copies each to the one container user that reads it, labelled at the sign-in's own SELinux level, and the
// containers get them as files. No secret is ever on a command line, in a file the agent writes in the clear, or in
// an answer (ADR 0011).
func TestTheSignInRunsAsOneUnitWithItsSecretsSealedForIt(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := joined(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}})

	code, body := call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik", "bootstrapToken": bootstrap})
	if code != http.StatusOK {
		t.Fatalf("the sign-in: %d %s", code, body)
	}
	if bytes.Contains(body, []byte(bootstrap)) {
		t.Errorf("the answer carries the bootstrap token: %s", body)
	}
	var s agent.SignInStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if s.Provider != "authentik" || s.Upstream != "http://172.31.250.10:9000" || s.Unit != "homerslab-authentik.service" || !s.Bootstrapping {
		t.Errorf("status %+v", s)
	}

	sealed := map[string][]byte{}
	for _, name := range []string{"authentik.secret-key", "authentik.postgres-password", "authentik.bootstrap-token"} {
		b, err := os.ReadFile(filepath.Join(state, "sign-in", name+".cred"))
		if err != nil {
			t.Fatalf("%s isn't sealed: %v", name, err)
		}
		sealed[name] = b
	}
	if got, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(sealed["authentik.bootstrap-token"]), "sealed:")); string(got) != bootstrap {
		t.Error("the bootstrap token sealed isn't the manager's")
	}
	for _, c := range h.Cmds() {
		if strings.Contains(c.String(), bootstrap) {
			t.Errorf("a secret was on a command line: %s", c.Name)
		}
		if c.Name == "systemd-creds" && c.Args[0] == "encrypt" && !strings.Contains(strings.Join(c.Args, " "), "--with-key=host") {
			t.Errorf("sealed without naming its key: %v", c.Args)
		}
	}

	d := signInDropIn(t, h)
	for _, want := range []string{
		"LoadCredentialEncrypted=authentik.secret-key:" + filepath.Join(state, "sign-in", "authentik.secret-key.cred") + "\n",
		"LoadCredentialEncrypted=authentik.postgres-password:" + filepath.Join(state, "sign-in", "authentik.postgres-password.cred") + "\n",
		"LoadCredentialEncrypted=authentik.bootstrap-token:" + filepath.Join(state, "sign-in", "authentik.bootstrap-token.cred") + "\n",
		// Each file for the one container user that reads it: Authentik's (1000), PostgreSQL's (70).
		"ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 %d/authentik.secret-key /run/homerslab-authentik/secret-key\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 %d/authentik.postgres-password /run/homerslab-authentik/postgres-password\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 70 -g 70 %d/authentik.postgres-password /run/homerslab-authentik/postgres-password-db\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 %d/authentik.bootstrap-token /run/homerslab-authentik/bootstrap-token\n",
		"ExecStartPre=/usr/bin/chcon -t container_file_t -l s0:c1000,c1001 /run/homerslab-authentik/secret-key /run/homerslab-authentik/postgres-password /run/homerslab-authentik/postgres-password-db /run/homerslab-authentik/bootstrap-token\n",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("the sign-in's drop-in lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, bootstrap) {
		t.Error("the drop-in holds the bootstrap token")
	}
	reload, enable, restart := h.Ran("systemctl daemon-reload"), h.Ran("systemctl enable homerslab-authentik.service"), h.Ran("systemctl --no-block restart homerslab-authentik.service")
	if reload < 0 || enable < reload || restart < enable {
		t.Errorf("reload %d, enable %d, restart %d: the unit gets its credentials only as it starts", reload, enable, restart)
	}

	t.Run("its own secrets are made once: asking again keeps them", func(t *testing.T) {
		if code, body := call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik"}); code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		for _, name := range []string{"authentik.secret-key", "authentik.postgres-password"} {
			b, _ := os.ReadFile(filepath.Join(state, "sign-in", name+".cred"))
			if !bytes.Equal(b, sealed[name]) {
				t.Errorf("%s changed: Authentik's data would no longer open", name)
			}
		}
		if _, err := os.Stat(filepath.Join(state, "sign-in", "authentik.bootstrap-token.cred")); err != nil {
			t.Error("a request without a bootstrap token dropped the one given")
		}
	})

	t.Run("the bootstrap token goes once it has done its work, and the unit restarts without it", func(t *testing.T) {
		before := h.Times("systemctl --no-block restart homerslab-authentik.service")
		if code, body := call(t, sock, "DELETE", "/v1/sign-in/bootstrap-token", nil); code != http.StatusNoContent {
			t.Fatalf("%d %s", code, body)
		}
		if _, err := os.Stat(filepath.Join(state, "sign-in", "authentik.bootstrap-token.cred")); err == nil {
			t.Error("the bootstrap token is still sealed")
		}
		d := signInDropIn(t, h)
		if strings.Contains(d, "authentik.bootstrap-token:") {
			t.Errorf("the unit still loads the bootstrap token:\n%s", d)
		}
		if !strings.Contains(d, "ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 /dev/null /run/homerslab-authentik/bootstrap-token\n") {
			t.Errorf("the unit doesn't empty the bootstrap file:\n%s", d)
		}
		if h.Times("systemctl --no-block restart homerslab-authentik.service") == before {
			t.Error("the unit wasn't restarted")
		}
		_, body := call(t, sock, "GET", "/v1/sign-in", nil)
		var s agent.SignInStatus
		_ = json.Unmarshal(body, &s)
		if s.Bootstrapping {
			t.Errorf("status %+v", s)
		}
	})

	for name, req := range map[string]map[string]string{
		"another provider":               {"provider": "kanidm"},
		"a bootstrap token on two lines": {"provider": "authentik", "bootstrapToken": "a\nb"},
	} {
		if code, _ := call(t, sock, "PUT", "/v1/sign-in", req); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
}

// Without SELinux, nothing is labelled: chcon would fail on a kernel without it.
func TestTheSignInsSecretsAreLabelledOnlyUnderSELinux(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := joined(t, agent.Config{})
	if code, body := call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik"}); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if d := signInDropIn(t, h); strings.Contains(d, "chcon") {
		t.Errorf("labels without SELinux:\n%s", d)
	}
}

// The agent reads the sign-in's state from systemd and its health from Authentik's own readiness check, never through
// an engine, and the unit's logs from the journal.
func TestTheSignInsStatusComesFromItsUnitAndItsHealthCheck(t *testing.T) {
	testenv.Isolate(t)
	ready := http.StatusServiceUnavailable
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/health/ready/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(ready)
	}))
	defer up.Close()
	sock, h, _ := joined(t, agent.Config{SignInUpstream: strings.TrimPrefix(up.URL, "http://")})
	if code, body := call(t, sock, "GET", "/v1/sign-in", nil); code != http.StatusNotFound {
		t.Errorf("before it is installed: %d %s", code, body)
	}
	call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik"})
	status := func() agent.SignInStatus {
		_, body := call(t, sock, "GET", "/v1/sign-in", nil)
		var s agent.SignInStatus
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); !s.Active || s.Ready {
		t.Errorf("starting: %+v", s)
	}
	ready = http.StatusOK
	if s := status(); !s.Active || !s.Ready {
		t.Errorf("ready: %+v", s)
	}
	h.Fail("systemctl is-active")
	if s := status(); s.Active {
		t.Errorf("a stopped unit: %+v", s)
	}
	h.Mend()
	code, body := call(t, sock, "GET", "/v1/sign-in/logs", nil)
	if code != http.StatusOK || h.Ran("journalctl --unit=homerslab-authentik.service --lines=200 --no-pager --output=short-iso") < 0 {
		t.Errorf("the logs: %d", code)
	}
	// An Authentik that logged requests (before its log level was lowered) wrote enrolment links' tokens: the logs
	// endpoint never hands one on (review of #17).
	if strings.Contains(string(body), agenttest.JournalToken) || !strings.Contains(string(body), "homerslab-passkey-enrolment") {
		t.Errorf("the logs hand on an enrolment link's token, or lost its line: %s", body)
	}
}

// The edge serves the sign-in only once the manager says so, after the provider's first-admin flow is closed: its
// route goes to the sign-in's upstream.
func TestTheEdgeServesTheSignInWhenTheManagerSaysSo(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := joined(t, agent.Config{})
	chain, key := wildcard(t, "example.test")
	call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test"})
	call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key})
	call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik", "bootstrapToken": bootstrap})
	if bytes.Contains(h.Edge.Config(), []byte("auth.example.test")) {
		t.Fatal("the edge serves the sign-in before the manager said so")
	}
	if code, body := call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test", SignIn: "auth.example.test"}); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	want, _ := edge.Render(edge.Config{AdminSocket: agent.DefaultEdge.AdminSocket, Storage: "/var/lib/homerslab-edge",
		Listen: []netip.Addr{netip.MustParseAddr("100.88.7.186")}, Port: 443,
		Certificates: []edge.Certificate{{Domain: "example.test", CertFile: "/run/credentials/homerslab-edge.service/tls-example.test.crt",
			KeyFile: "/run/credentials/homerslab-edge.service/tls-example.test.key"}},
		Admin:  &edge.Site{Name: "admin.example.test", Upstream: "/run/homerslab-admin.sock"},
		SignIn: &edge.Site{Name: "auth.example.test", Dial: "172.31.250.10:9000"}})
	if got := h.Edge.Config(); !edge.Same(withoutAdmin(t, got), withoutAdmin(t, want)) {
		t.Errorf("the edge runs:\n%s\nwant:\n%s", got, want)
	}
}
