package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
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
	if table, ok := h2.Table(); !ok || strings.Contains(table, "7020") || !strings.Contains(table, `iifname { "wt0" } accept`) {
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

func TestAMachineWithATPM12SealsWithTheHostKey(t *testing.T) {
	testenv.Isolate(t)
	sysroot := fstest.MapFS{"sys/class/tpm/tpm0/tpm_version_major": {Data: []byte("1\n")}}
	sock, h := agenttest.StartWith(t, agent.Config{Sysroot: sysroot})
	call(t, sock, "PUT", "/v1/credentials/manager-mesh-token", map[string]string{"value": "x"})
	if args := strings.Join(h.Cmds()[h.Ran("systemd-creds encrypt")].Args, " "); !strings.Contains(args, "--with-key=host ") {
		t.Errorf("systemd-creds %s, want --with-key=host: systemd can't seal to a TPM 1.2", args)
	}
}

func TestACredentialCanBeForgotten(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state})
	call(t, sock, "PUT", "/v1/credentials/manager-mesh-setup-token", map[string]string{"value": "x"})
	if code, _ := call(t, sock, "DELETE", "/v1/credentials/manager-mesh-setup-token", nil); code != http.StatusNoContent {
		t.Errorf("forgetting: %d", code)
	}
	if code, _ := call(t, sock, "GET", "/v1/credentials/manager-mesh-setup-token", nil); code != http.StatusNotFound {
		t.Errorf("after forgetting: %d, want 404", code)
	}
	if code, _ := call(t, sock, "DELETE", "/v1/credentials/photos-db-password", nil); code != http.StatusNotFound {
		t.Errorf("an app's credential: %d, want 404", code)
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

func TestAnAgentThatCantApplyItsTableKeepsTryingAndServes(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	h := agenttest.NewHost(t.TempDir())
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())

	// After a reboot nft fails at first (a module still loading): the agent comes up anyway, and the next check
	// applies the table.
	h2 := agenttest.NewHost(t.TempDir())
	h2.Fail("nft -f")
	sock2, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h2.Run, Root: h2.Root})
	if _, on := h2.Table(); on {
		t.Fatal("a table, though nft failed")
	}
	h2.Mend()
	if code, body := call(t, sock2, "POST", "/v1/firewall/check", nil); code != http.StatusOK {
		t.Fatalf("check: %d %s", code, body)
	}
	if table, on := h2.Table(); !on || !strings.Contains(table, `iifname { "wt0" } accept`) {
		t.Errorf("the check didn't apply the table:\n%s", table)
	}
}

func TestClosingTheSetupListenerHoldsEvenWhenTheFirstTryFails(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	h.Fail("nft -f")
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false}); code != http.StatusInternalServerError {
		t.Errorf("closing with nft failing: %d, want 500", code)
	}
	h.Mend()
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false}); code != http.StatusOK {
		t.Errorf("closing again: %d", code)
	}
	if table, _ := h.Table(); strings.Contains(table, "7020") {
		t.Errorf("after the retry the table still lets the setup listener in:\n%s", table)
	}
}

func TestTheTableFollowsTheLANAndTheEnginesBridges(t *testing.T) {
	testenv.Isolate(t)
	var mu sync.Mutex
	prefix := "192.168.1.0/24"
	lan := func() ([]string, []netip.Prefix) {
		mu.Lock()
		defer mu.Unlock()
		return []string{"br-iot"}, []netip.Prefix{netip.MustParsePrefix(prefix)}
	}
	// Podman, from its network files: its default bridge and one network.
	sysroot := fstest.MapFS{
		"usr/bin/podman":                      {Data: []byte{}},
		"etc/containers/networks/photos.json": {Data: []byte(`{"name":"photos","driver":"bridge","network_interface":"podman1"}`)},
		"etc/containers/networks/mac.json":    {Data: []byte(`{"name":"mac","driver":"macvlan","network_interface":"enp5s0"}`)},
		"etc/resolv.conf":                     {Data: []byte("nameserver 127.0.0.53\n")},
		"run/systemd/resolve/resolv.conf":     {Data: []byte("nameserver 192.168.1.1\nnameserver 2001:db8:1::1\n")},
	}
	sock, h := agenttest.StartWith(t, agent.Config{LAN: lan, Sysroot: sysroot})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	// Docker's default bridge, a compose network it named br-<id>, one it was told to call br-iot (the LAN's own
	// name), names nft would misread, and a name that is the LAN's NIC, not a bridge.
	h.Engines(map[string]string{"0123456789abcdef": "docker0", "abcdef0123456789": "", "fedcba9876543210": "br-iot",
		"1111111111111111": "en*", "2222222222222222": `x"y`, "3333333333333333": "enp5s0"})
	h.NotABridge("enp5s0")
	mu.Lock()
	prefix = "2001:db8:5::/64"
	mu.Unlock()
	_, body := call(t, sock, "POST", "/v1/firewall/check", nil)
	table, _ := h.Table()
	if !strings.Contains(table, `iifname { "br-abcdef012345", "docker0", "podman0", "podman1" } jump apps_in`) {
		t.Errorf("the engines' bridges, by their exact names, and never the LAN's:\n%s", table)
	}
	for _, bad := range []string{"en*", `x\"y`, `"enp5s0"`} {
		if strings.Contains(table, bad) {
			t.Errorf("rendered %s:\n%s", bad, table)
		}
	}
	if !strings.Contains(table, "2001:db8:5::/64") {
		t.Errorf("the table didn't follow the LAN's new prefix:\n%s", table)
	}
	if !strings.Contains(table, "ip daddr { 192.168.1.1 } meta l4proto { tcp, udp } th dport 53 accept") ||
		!strings.Contains(table, "ip6 daddr { 2001:db8:1::1 } meta l4proto { tcp, udp } th dport 53 accept") {
		t.Errorf("the apps can't ask the machine's resolvers:\n%s", table)
	}
	var s agent.FirewallStatus
	_ = json.Unmarshal(body, &s)
	var refused []string
	for _, d := range s.Drift {
		refused = append(refused, d.What)
	}
	if !slices.Contains(refused, `refused-bridge "en*"`) || !slices.Contains(refused, `refused-bridge "enp5s0"`) ||
		!slices.Contains(refused, `refused-bridge "x\"y"`) {
		t.Errorf("refused names reported: %v", refused)
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if _, body := call(t, sock, "GET", "/v1/firewall", nil); strings.Count(string(body), "refused-bridge") != 3 {
		t.Errorf("a refused name is reported more than once: %s", body)
	}
	h.Engines(nil) // Docker stops
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if table, _ := h.Table(); strings.Contains(table, "docker0") || !strings.Contains(table, "podman1") {
		t.Errorf("Docker's bridges are still trusted once it stopped:\n%s", table)
	}
}

func TestTheAgentWritesTheTableItAppliesForTheNextBoot(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false})
	saved, err := os.ReadFile(filepath.Join(state, "firewall.nft"))
	table, _ := h.Table()
	if err != nil || string(saved) != table {
		t.Fatalf("saved %v:\n%s\napplied:\n%s", err, saved, table)
	}
	if fi, _ := os.Stat(filepath.Join(state, "firewall.nft")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", fi.Mode().Perm())
	}
}

func TestReadyNeverWaitsOnAnEngine(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	h.Engines(map[string]string{"abcdef0123456789": ""})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "POST", "/v1/firewall/check", nil)

	// After a reboot Docker is socket-activated and waits for the agent: asking it would never return.
	notify := filepath.Join(t.TempDir(), "notify.sock")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("NOTIFY_SOCKET", notify)
	h2 := agenttest.NewHost(t.TempDir())
	h2.Block()
	sock2, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h2.Run, Root: h2.Root, CheckEvery: 50 * time.Millisecond})
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if n, err := ln.Read(buf); err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("no READY with the engines hanging: %q %v", buf[:n], err)
	}
	if table, _ := h2.Table(); !strings.Contains(table, `"br-abcdef012345"`) {
		t.Errorf("the restored table lost the bridges it knew:\n%s", table)
	}
	// With the watch loop stuck asking a hanging engine, the agent still answers at once.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if code, _ := call(t, sock2, "GET", "/v1/firewall", nil); code != http.StatusOK || time.Since(start) > time.Second {
		t.Errorf("GET /v1/firewall took %v with an engine hanging", time.Since(start))
	}
}

func TestTheJoinTakesTheManagersSetupPort(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	r := joinBody()
	r.SetupPort = 7444
	call(t, sock, "POST", "/v1/mesh/join", r)
	if table, _ := h.Table(); !strings.Contains(table, "tcp dport 7444 accept") || strings.Contains(table, "7020") {
		t.Errorf("the setup listener's port isn't the manager's:\n%s", table)
	}
}

func TestTheNATMapperSettingIsWatchedAndPutBack(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	mesh := func() agent.MeshStatus {
		t.Helper()
		_, body := call(t, sock, "GET", "/v1/mesh", nil)
		var s agent.MeshStatus
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := mesh(); !s.Joined || !s.PortMappingOff {
		t.Fatalf("after the join: %+v", s)
	}
	h.RestartNetBirdWithoutItsDropIn()
	if s := mesh(); s.PortMappingOff {
		t.Error("the report says the NAT mapper is off while the client runs without the drop-in")
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	s := mesh()
	if !s.PortMappingOff || len(s.Drift) != 1 || s.Drift[0].What != "nat-mapper-drop-in" {
		t.Errorf("after the check: %+v", s)
	}
	h.RestartNetBirdBeforeItsDropIn()
	if s := mesh(); s.PortMappingOff {
		t.Error("the report says the NAT mapper is off while the running client lacks the setting")
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if s := mesh(); !s.PortMappingOff || len(s.Drift) != 2 || s.Drift[1].What != "nat-mapper-environment" {
		t.Errorf("after the second check: %+v", s)
	}
}

func TestTheAgentTellsSystemdItIsReadyOnceItsTableIsBack(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())

	notify := filepath.Join(t.TempDir(), "notify.sock")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("NOTIFY_SOCKET", notify)
	h := agenttest.NewHost(t.TempDir())
	h.Fail("nft -f") // at boot nft fails at first: no READY until the table is in place
	agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root})
	buf := make([]byte, 64)
	_ = ln.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	if n, err := ln.Read(buf); err == nil {
		t.Fatalf("systemd heard %q before the table was in place", buf[:n])
	}
	h.Mend()
	_ = ln.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, err := ln.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("systemd heard %q, %v", buf[:n], err)
	}
	if _, on := h.Table(); !on {
		t.Error("ready before the firewall table was back")
	}
}

func TestTheSetupKeyNeverReachesTheLogs(t *testing.T) {
	testenv.Isolate(t)
	for _, failing := range []string{"", "netbird up", "netbird status", "nft -f"} {
		var logs bytes.Buffer
		h := agenttest.NewHost(t.TempDir())
		if failing != "" {
			h.Fail(failing)
		}
		log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sock, _ := agenttest.StartWith(t, agent.Config{Run: h.Run, Root: h.Root, Log: log})
		call(t, sock, "POST", "/v1/mesh/join", joinBody())
		call(t, sock, "PUT", "/v1/credentials/manager-mesh-token", map[string]string{"value": "nbp_SEALME"})
		call(t, sock, "GET", "/v1/credentials/manager-mesh-token", nil)
		if strings.Contains(logs.String(), setupKey) || strings.Contains(logs.String(), "nbp_SEALME") {
			t.Errorf("with %q failing, a secret is in the agent's log:\n%s", failing, logs.String())
		}
	}
}

// Once NetBird's client runs with the drop-in's setting, checking again restarts nothing: the agent tells by asking
// systemd, as it can't read the client's own environment (a root process with every capability, behind
// ProtectProc=invisible) (#99, the fifth review).
func TestTheNATMapperSettingOnceAppliedIsLeftAlone(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	after := h.NetBirdRestarts()
	for range 5 {
		call(t, sock, "POST", "/v1/firewall/check", nil)
	}
	if n := h.NetBirdRestarts() - after; n != 0 {
		t.Errorf("five checks after the join restarted NetBird %d times, want none", n)
	}
	_, body := call(t, sock, "GET", "/v1/mesh", nil)
	var s agent.MeshStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if !s.PortMappingOff || len(s.Drift) != 0 {
		t.Errorf("after the checks: %+v, want the NAT mapper off and no drift", s)
	}
}

// The agent never opens another process's /proc/<pid>/environ: the kernel refuses it to a reader without every
// capability of the target's, and the agent's unit hides other processes (ProtectProc=invisible).
func TestTheAgentNeverReadsAnotherProcesssEnvironment(t *testing.T) {
	testenv.Isolate(t)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// In code, not in comments: a path in a string ends with "environ", or a path starts at "/proc" but for the
		// process's own (/proc/self, which a read's child uses on itself).
		b = bytes.ReplaceAll(b, []byte(`"/proc/self/`), nil)
		if bytes.Contains(b, []byte(`environ"`)) || bytes.Contains(b, []byte(`"/proc`)) {
			t.Errorf("%s reads a process's environment from /proc", f)
		}
	}
}

// The client runs with the drop-in's environment only when systemd has loaded the drop-in as it is now, and applies it
// to the unit.
func TestTheNATMapperSettingNeedsSystemdToHaveLoadedIt(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		what string
		do   func(*agenttest.Host)
	}{
		{"systemd runs an older drop-in, not reloaded", (*agenttest.Host).NetBirdLoadedAnOlderDropIn},
		{"systemd applies no drop-in to the unit", (*agenttest.Host).NetBirdIgnoresItsDropIn},
	} {
		t.Run(c.what, func(t *testing.T) {
			sock, h := agenttest.StartWith(t, agent.Config{})
			call(t, sock, "POST", "/v1/mesh/join", joinBody())
			c.do(h)
			_, body := call(t, sock, "GET", "/v1/mesh", nil)
			var s agent.MeshStatus
			if err := json.Unmarshal(body, &s); err != nil {
				t.Fatal(err)
			}
			if s.PortMappingOff {
				t.Errorf("%s: the report says the NAT mapper is off", c.what)
			}
		})
	}
}

// On a systemd that can't say when the client started (older than 248), the agent can't tell whether the setting is
// applied: it restarts nothing, and reports that once.
func TestTheNATMapperSettingIsUnknownWhenSystemdCannotSayAndLeftAlone(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	h.NetBirdSystemdTooOld()
	after := h.NetBirdRestarts()
	for range 5 {
		call(t, sock, "POST", "/v1/firewall/check", nil)
	}
	if n := h.NetBirdRestarts() - after; n != 0 {
		t.Errorf("five checks on an old systemd restarted NetBird %d times, want none", n)
	}
	_, body := call(t, sock, "GET", "/v1/mesh", nil)
	var s agent.MeshStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if s.PortMappingOff || len(s.Drift) != 1 || s.Drift[0].What != "nat-mapper-unknown" {
		t.Errorf("after the checks: %+v, want the NAT mapper not reported off and one nat-mapper-unknown drift", s)
	}
}
