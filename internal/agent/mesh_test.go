package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const setupKey = "A1B2C3D4-0000-4000-8000-SETUPKEY0001"

func call(t *testing.T, sock, method, path string, body any) (int, []byte) {
	t.Helper()
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	var r *bytes.Reader = bytes.NewReader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://agent"+path, r)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func joinBody() agent.JoinRequest {
	return agent.JoinRequest{Mesh: "netbird", ManagementURL: "https://netbird.ferreira.family", SetupKey: setupKey, Hostname: "study-pc"}
}

func status(t *testing.T, sock string) agent.FirewallStatus {
	t.Helper()
	code, body := call(t, sock, "GET", "/v1/firewall", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/firewall: %d %s", code, body)
	}
	var s agent.FirewallStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTheNodeJoinsWithItsSetupKeyAndNATMapperOff(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})

	if s := status(t, sock); s.Enforcing {
		t.Fatal("the firewall table is on before the node joined its mesh")
	}
	code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody())
	if code != http.StatusOK {
		t.Fatalf("join: %d %s", code, body)
	}
	var j agent.Joined
	_ = json.Unmarshal(body, &j)
	if j.Interface != "wt0" || j.IP.String() != "100.88.7.186" {
		t.Errorf("joined: %+v", j)
	}
	if bytes.Contains(body, []byte(setupKey)) {
		t.Error("the answer carries the setup key")
	}

	up := h.Ran("netbird up")
	if up < 0 {
		t.Fatal("never ran netbird up")
	}
	if restart := h.Ran("systemctl restart netbird.service"); restart < 0 || restart > up || h.Ran("systemctl daemon-reload") > restart {
		t.Errorf("NetBird's service wasn't reloaded and restarted before it joined: %v", h.Cmds())
	}
	if d := string(h.DropInWhen("netbird up")); !strings.Contains(d, "[Service]\nEnvironment=NB_DISABLE_NAT_MAPPER=true\n") {
		t.Errorf("when the client joined, its drop-in was %q: its NAT mapper would ask the router for a port", d)
	}
	cmd := h.Cmds()[up]
	if strings.Contains(strings.Join(cmd.Args, " "), setupKey) {
		t.Errorf("the setup key is on the command line, for every user to read: %v", cmd.Args)
	}
	if !strings.Contains(strings.Join(cmd.Env, " "), "NB_SETUP_KEY="+setupKey) {
		t.Error("the setup key never reached the client")
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "--management-url https://netbird.ferreira.family") {
		t.Errorf("netbird up %v", cmd.Args)
	}

	s := status(t, sock)
	if !s.Enforcing || s.State.SetupPort != 7020 || len(s.State.Mesh) != 1 || s.State.Mesh[0] != "wt0" {
		t.Errorf("after the join the firewall table is %+v", s)
	}
	if table, ok := h.Table(); !ok || table != firewall.Render(s.State) {
		t.Errorf("the table applied isn't the one rendered:\n%s", table)
	}
}

func TestAJoinIsRefusedWhenUnsafe(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	for name, edit := range map[string]func(*agent.JoinRequest){
		"plain HTTP to another machine": func(r *agent.JoinRequest) { r.ManagementURL = "http://netbird.ferreira.family" },
		"an unknown mesh":               func(r *agent.JoinRequest) { r.Mesh = "zerotier" },
		"no key":                        func(r *agent.JoinRequest) { r.SetupKey = "" },
		"a hostname that's an option":   func(r *agent.JoinRequest) { r.Hostname = "--setup-key=x" },
	} {
		r := joinBody()
		edit(&r)
		if code, body := call(t, sock, "POST", "/v1/mesh/join", r); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}
	if h.Ran("netbird up") >= 0 {
		t.Error("a refused join ran netbird up")
	}
}

func TestAFailedJoinLeavesTheFirewallAsItWas(t *testing.T) {
	testenv.Isolate(t)
	for _, step := range []string{"netbird up", "netbird status"} {
		t.Run(step, func(t *testing.T) {
			testenv.Isolate(t)
			h := agenttest.NewHost(t.TempDir())
			h.Fail(step)
			sock, _ := agenttest.StartWith(t, agent.Config{Run: h.Run, Root: h.Root})
			code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody())
			if code != http.StatusBadGateway || bytes.Contains(body, []byte(setupKey)) {
				t.Errorf("join with %s failing: %d %s", step, code, body)
			}
			if _, on := h.Table(); on || status(t, sock).Enforcing {
				t.Error("the firewall table went on for a node that never joined")
			}
		})
	}
}

func TestAJoinThatBreaksTheMachinesResolverFails(t *testing.T) {
	testenv.Isolate(t)
	broken := func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{Err: "no answer", IsTimeout: true}
	}
	sock, h := agenttest.StartWith(t, agent.Config{Resolve: broken})
	if code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody()); code != http.StatusBadGateway || !bytes.Contains(body, []byte("resolver-broken")) {
		t.Errorf("join with the machine's resolver broken: %d %s", code, body)
	}
	if _, on := h.Table(); on {
		t.Error("the firewall table went on")
	}
}

func TestTheAgentCorrectsDriftInTheFirewallTableAndReportsIt(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	if code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody()); code != http.StatusOK {
		t.Fatalf("join: %d %s", code, body)
	}
	want, _ := h.Table()
	for _, how := range []string{"changed", "missing"} {
		h.Tamper(how)
		code, body := call(t, sock, "POST", "/v1/firewall/check", nil)
		if code != http.StatusOK {
			t.Fatalf("check: %d %s", code, body)
		}
		if got, ok := h.Table(); !ok || got != want {
			t.Errorf("after the table was %s, the check left:\n%s", how, got)
		}
	}
	s := status(t, sock)
	if len(s.Drift) != 2 || s.Drift[0].What != "changed" || s.Drift[1].What != "missing" {
		t.Errorf("drift reported: %+v", s.Drift)
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if s := status(t, sock); len(s.Drift) != 2 {
		t.Errorf("a check of an unchanged table reported drift: %+v", s.Drift)
	}
}

func TestTheAgentWatchesItsTableByItself(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{CheckEvery: 20 * time.Millisecond})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	h.Tamper("missing")
	for range 200 {
		if _, ok := h.Table(); ok && len(status(t, sock).Drift) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the agent never put its table back by itself")
}

func TestTheSetupListenerClosesForGoodAndStaysClosed(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	h := agenttest.NewHost(t.TempDir())
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())

	if code, body := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false}); code != http.StatusOK {
		t.Fatalf("closing: %d %s", code, body)
	}
	s := status(t, sock)
	if s.SetupListener.Open || s.State.SetupPort != 0 {
		t.Errorf("after closing: %+v", s)
	}
	if table, _ := h.Table(); strings.Contains(table, "7020") {
		t.Errorf("the table still lets the setup listener in:\n%s", table)
	}
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": true}); code != http.StatusConflict {
		t.Errorf("reopening: %d, want 409", code)
	}

	// A restart, as after a power cut: the table comes back, still closed.
	h2 := agenttest.NewHost(t.TempDir())
	sock2, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h2.Run, Root: h2.Root})
	if table, ok := h2.Table(); !ok || strings.Contains(table, "7020") || !strings.Contains(table, `iifname "wt0" accept`) {
		t.Errorf("after a restart the table is (%v):\n%s", ok, table)
	}
	if s := status(t, sock2); s.SetupListener.Open || !s.Enforcing {
		t.Errorf("after a restart: %+v", s)
	}
}

func TestTheManagersCredentialsAreSealedAndOnlyItsOwnComeBack(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	const token = "nbp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	if code, body := call(t, sock, "PUT", "/v1/credentials/manager-mesh-token", map[string]string{"value": token}); code != http.StatusNoContent {
		t.Fatalf("sealing: %d %s", code, body)
	}
	enc := h.Cmds()[h.Ran("systemd-creds encrypt")]
	if got := strings.Join(enc.Args, " "); !strings.Contains(got, "--with-key=host+tpm2") || !strings.Contains(got, "--name=manager-mesh-token") || strings.Contains(got, token) {
		t.Errorf("systemd-creds %s: want the TPM and the host key explicitly, the name, and the value on stdin only", got)
	}
	b, err := os.ReadFile(filepath.Join(state, "credentials", "manager-mesh-token.cred"))
	if err != nil || bytes.Contains(b, []byte(token)) {
		t.Errorf("the sealed file: %v, holds the token in the clear: %v", err, bytes.Contains(b, []byte(token)))
	}
	code, body := call(t, sock, "GET", "/v1/credentials/manager-mesh-token", nil)
	var got struct{ Value string }
	_ = json.Unmarshal(body, &got)
	if code != http.StatusOK || got.Value != token {
		t.Errorf("unsealing: %d", code)
	}
	for _, name := range []string{"photos-db-password", "manager-../x", "MANAGER-x", "manager-"} {
		if code, _ := call(t, sock, "GET", "/v1/credentials/"+name, nil); code != http.StatusNotFound {
			t.Errorf("GET credential %q: %d, want 404", name, code)
		}
		if code, _ := call(t, sock, "PUT", "/v1/credentials/"+name, map[string]string{"value": "x"}); code != http.StatusNotFound {
			t.Errorf("PUT credential %q: %d, want 404", name, code)
		}
	}
}

func TestAMachineWithoutTPMSealsWithTheHostKey(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{Sysroot: os.DirFS(t.TempDir())})
	call(t, sock, "PUT", "/v1/credentials/manager-mesh-token", map[string]string{"value": "x"})
	if args := strings.Join(h.Cmds()[h.Ran("systemd-creds encrypt")].Args, " "); !strings.Contains(args, "--with-key=host ") {
		t.Errorf("systemd-creds %s, want --with-key=host", args)
	}
}
