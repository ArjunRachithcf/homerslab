package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
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
	var h *agenttest.Host
	network := func() (lan.Network, error) {
		mu.Lock()
		defer mu.Unlock()
		return lan.Network{LAN: []string{"br-iot"}, Prefixes: []netip.Prefix{netip.MustParsePrefix(prefix)},
			Uplinks4: []string{"br-iot"}, Uplinks6: []string{"br-iot"}, Virtual: h.Guests()}, nil
	}
	// Podman, from its network files: its default bridge and one network.
	sysroot := fstest.MapFS{
		"usr/bin/podman":                      {Data: []byte{}},
		"etc/containers/networks/photos.json": {Data: []byte(`{"name":"photos","driver":"bridge","network_interface":"podman1"}`)},
		"etc/containers/networks/mac.json":    {Data: []byte(`{"name":"mac","driver":"macvlan","network_interface":"enp5s0"}`)},
		"etc/resolv.conf":                     {Data: []byte("nameserver 127.0.0.53\n")},
		"run/systemd/resolve/resolv.conf":     {Data: []byte("nameserver 192.168.1.1\nnameserver 2001:db8:1::1\n")},
	}
	sock, h0 := agenttest.StartWith(t, agent.Config{Network: network, Sysroot: sysroot})
	mu.Lock()
	h = h0
	mu.Unlock()
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
	if !strings.Contains(table, `iifname { "br-abcdef012345", "docker0", "podman0", "podman1" } meta iifkind "bridge" jump apps_in`) {
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
	if !strings.Contains(table, `oifname { "br-iot" } ip daddr { 192.168.1.1 } meta l4proto { tcp, udp } th dport 53 accept`) ||
		!strings.Contains(table, `oifname { "br-iot" } ip6 daddr { 2001:db8:1::1 } meta l4proto { tcp, udp } th dport 53 accept`) {
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
	// Docker stops. Its bridges stay in the kernel (it doesn't delete them; with live-restore its containers run on):
	// they stay app bridges, never a local virtual network each, until they leave the kernel.
	h.Engines(nil)
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if table, _ := h.Table(); !strings.Contains(table, `"docker0"`) || !strings.Contains(table, "podman1") {
		t.Errorf("a stopped Docker's bridges, still in the kernel, aren't app bridges:\n%s", table)
	}
	h.NotABridge("docker0")
	h.NotABridge("br-abcdef012345")
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if table, _ := h.Table(); strings.Contains(table, "docker0") || !strings.Contains(appNetworks(table), "podman1") {
		t.Errorf("Docker's bridges are still trusted once they went:\n%s", table)
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

func TestAHungDockerKeepsItsBridges(t *testing.T) {
	testenv.Isolate(t)
	const show = "systemctl show -p ActiveState -p LoadState docker.service"
	for _, how := range []string{"errors", "times out", "systemd times out", "systemd errors", "activating", "deactivating",
		"reloading", "stopped with its containers running (live-restore)"} {
		t.Run(how, func(t *testing.T) {
			testenv.Isolate(t)
			sock, h := agenttest.StartWith(t, agent.Config{})
			call(t, sock, "POST", "/v1/mesh/join", joinBody())
			h.Engines(map[string]string{"0123456789abcdef": "docker0", "abcdef0123456789": ""})
			call(t, sock, "POST", "/v1/firewall/check", nil)
			if table, _ := h.Table(); !strings.Contains(table, `iifname { "br-abcdef012345", "docker0" } meta iifkind "bridge" jump apps_in`) {
				t.Fatalf("Docker's bridges while it answers:\n%s", table)
			}
			// Docker doesn't answer, or is on its way up or down, or stopped while its containers run on: its bridges
			// are the ones it last listed, not a local virtual network each.
			switch how {
			case "errors":
				h.Fail("docker network")
			case "times out":
				h.Hang("docker network")
			case "systemd times out": // asked whether Docker runs, systemd doesn't say
				h.Hang(show)
			case "systemd errors": // nor when it fails (no D-Bus, say)
				h.Fail(show)
			case "activating", "deactivating", "reloading": // at every boot and every restart of Docker
				h.DockerState(how, "loaded")
			default:
				h.Engines(nil)
			}
			call(t, sock, "POST", "/v1/firewall/check", nil)
			if table, _ := h.Table(); !strings.Contains(table, `iifname { "br-abcdef012345", "docker0" } meta iifkind "bridge" jump apps_in`) {
				t.Errorf("with Docker %s, its bridges went:\n%s", how, table)
			}
			// Still only what the kernel shows is a bridge; one that went is dropped, not reported as refused.
			h.NotABridge("br-abcdef012345")
			_, body := call(t, sock, "POST", "/v1/firewall/check", nil)
			if table, _ := h.Table(); !strings.Contains(table, `iifname { "docker0" } meta iifkind "bridge" jump apps_in`) {
				t.Errorf("a kept name the kernel no longer shows as a bridge is still trusted:\n%s", table)
			}
			if strings.Contains(string(body), "refused-bridge") {
				t.Errorf("a kept bridge that went is reported as an engine's refused name: %s", body)
			}
			// Docker answering again is the list from then on.
			h.Mend()
			h.DockerState("", "")
			h.Engines(map[string]string{"1111111111111111": "br-compose"})
			call(t, sock, "POST", "/v1/firewall/check", nil)
			// docker0, still in the kernel with its containers' veths, is a bridge nobody declared: a local virtual
			// network, no app network.
			if table, _ := h.Table(); strings.Contains(appNetworks(table), "docker0") || !strings.Contains(appNetworks(table), `"br-compose"`) {
				t.Errorf("once Docker answers again, its answer isn't the list:\n%s", table)
			}
			// Docker removed, or masked so it can never run: none, whatever the kernel still shows.
			for _, load := range []string{"masked", "not-found"} {
				h.DockerState("", "")
				call(t, sock, "POST", "/v1/firewall/check", nil)
				h.DockerState("inactive", load)
				call(t, sock, "POST", "/v1/firewall/check", nil)
				if table, _ := h.Table(); strings.Contains(appNetworks(table), "br-compose") {
					t.Errorf("Docker's bridges are still trusted once it is %s:\n%s", load, table)
				}
			}
		})
	}
}

func TestAnAgentWhoseStateIsBrokenStartsAndSaysSo(t *testing.T) {
	testenv.Isolate(t)
	for name, content := range map[string]string{"zero-length": "", "cut short": `{"enforcing":tr`} {
		t.Run(name, func(t *testing.T) {
			testenv.Isolate(t)
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, "agent.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			// A power cut just after the first join: the agent starts (no crash loop), and leaves the table boot loaded.
			sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
			s := status(t, sock)
			if !s.StateUnreadable || s.Enforcing {
				t.Errorf("with a broken state: %+v", s)
			}
			if _, on := h.Table(); on {
				t.Error("the agent replaced the table boot loaded, knowing nothing of its own")
			}
			// The manager's setup port, given before anything rejoins, is kept, but the state is still lost: said
			// so, after a restart too.
			call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": 7444})
			if s := status(t, sock); !s.StateUnreadable {
				t.Errorf("after the manager gave its port, the broken state isn't reported: %+v", s)
			}
			// The broken file was put aside first, as it was, for whoever looks into it.
			aside, _ := filepath.Glob(filepath.Join(state, "agent.json.unreadable-*"))
			if len(aside) != 1 {
				t.Fatalf("put aside: %v", aside)
			}
			if b, err := os.ReadFile(aside[0]); err != nil || string(b) != content {
				t.Errorf("the file put aside holds %q, %v; want %q", b, err, content)
			}
			sock2, _ := agenttest.StartWith(t, agent.Config{StateDir: state})
			if s := status(t, sock2); !s.StateUnreadable || s.SetupListener.Port != 7444 {
				t.Errorf("after a restart: %+v", s)
			}
			// The next join saves a whole state again.
			if code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody()); code != http.StatusOK {
				t.Fatalf("join: %d %s", code, body)
			}
			if s := status(t, sock); s.StateUnreadable || !s.Enforcing {
				t.Errorf("after the join: %+v", s)
			}
			var saved map[string]any
			if b, err := os.ReadFile(filepath.Join(state, "agent.json")); err != nil || json.Unmarshal(b, &saved) != nil || saved["enforcing"] != true {
				t.Errorf("the state after the join: %s, %v", b, err)
			}
		})
	}
}

func TestTheManagersSetupPortIsKeptForTheNextBoot(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	if code, body := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": 7444}); code != http.StatusOK {
		t.Fatalf("giving the port: %d %s", code, body)
	}
	if s := status(t, sock); s.SetupListener.Port != 7444 || !s.SetupListener.Open || s.Enforcing {
		t.Errorf("after the manager gave its port: %+v", s.SetupListener)
	}
	if port, ok := agent.SavedSetupPort(state); !ok || port != 7444 {
		t.Errorf("saved for the next boot's closed table: %d %v", port, ok)
	}
	if _, on := h.Table(); on {
		t.Error("a port given before the join applied a table")
	}
	for _, bad := range []int{-1, 0, 65536} {
		if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": bad}); code != http.StatusBadRequest {
			t.Errorf("port %d: %d, want 400", bad, code)
		}
	}
	// After the join, the table follows the port the manager gives; once closed, there is no port to give.
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	if table, _ := h.Table(); !strings.Contains(table, "tcp dport 7444 accept") {
		t.Errorf("the join's table lost the manager's port:\n%s", table)
	}
	call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": 7445})
	if table, _ := h.Table(); !strings.Contains(table, "tcp dport 7445 accept") || strings.Contains(table, "7444") {
		t.Errorf("the table didn't follow the manager's new port:\n%s", table)
	}
	call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false})
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": 7446}); code != http.StatusConflict {
		t.Errorf("a port given after closing: %d, want 409", code)
	}
	if port, ok := agent.SavedSetupPort(state); !ok || port != 0 {
		t.Errorf("closed for good, the next boot's closed table takes %d %v, want none", port, ok)
	}
}

func TestBridgesSavedByAnEarlierAgentAreKept(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "agent.json"),
		[]byte(`{"enforcing":true,"mesh":["wt0"],"wireGuard":[51820],"bridges":["br-abcdef012345","docker0"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := agenttest.NewHost(t.TempDir())
	h.Block() // at boot Docker hangs, waiting for the agent
	agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root})
	if table, _ := h.Table(); !strings.Contains(table, `iifname { "br-abcdef012345", "docker0" } meta iifkind "bridge" jump apps_in`) {
		t.Errorf("the bridges an earlier agent saved were lost:\n%s", table)
	}
}

// A state that can't be read for now (an I/O error, a wrong label) isn't a lost one: the agent waits for it, and never
// starts without it or writes over it.
func TestAStateThatCantBeReadForNowIsWaitedFor(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	good, _ := json.Marshal(map[string]any{"enforcing": true, "mesh": []string{"wt0"}, "wireGuard": []int{51820}})
	// agent.json is a link to a folder, so reading it fails (as with EIO); later the link is swapped, in one rename,
	// for one to a whole state.
	path := filepath.Join(state, "agent.json")
	dir, whole := filepath.Join(state, "a-folder"), filepath.Join(state, "whole.json")
	if err := errors.Join(os.Mkdir(dir, 0o700), os.WriteFile(whole, good, 0o600), os.Symlink(dir, path)); err != nil {
		t.Fatal(err)
	}
	mended := make(chan time.Time, 1)
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = os.Symlink(whole, path+".swap")
		_ = os.Rename(path+".swap", path)
		mended <- time.Now()
	}()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	up := time.Now()
	if at := <-mended; up.Before(at) {
		t.Errorf("the agent served %v before its state could be read", at.Sub(up))
	}
	if s := status(t, sock); !s.Enforcing || s.StateUnreadable {
		t.Errorf("once the state reads again: %+v", s)
	}
	if table, on := h.Table(); !on || !strings.Contains(table, `iifname { "wt0" } accept`) {
		t.Errorf("the table from the state:\n%s", table)
	}
	if aside, _ := filepath.Glob(filepath.Join(state, "agent.json.unreadable-*")); len(aside) != 0 {
		t.Errorf("a state that couldn't be read for a while was put aside: %v", aside)
	}
}

func TestALostStateIsPutAsideBeforeAJoinSavesOverIt(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "agent.json"), []byte(`{"enforc`), 0o600); err != nil {
		t.Fatal(err)
	}
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state})
	if code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody()); code != http.StatusOK {
		t.Fatalf("join: %d %s", code, body)
	}
	aside, _ := filepath.Glob(filepath.Join(state, "agent.json.unreadable-*"))
	if len(aside) != 1 {
		t.Fatalf("put aside: %v", aside)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != `{"enforc` {
		t.Errorf("put aside: %q", b)
	}
	if s := status(t, sock); s.StateUnreadable || !s.Enforcing {
		t.Errorf("after the join: %+v", s)
	}
}

func TestClosingAClosedListenerAgainLeavesTheTableAlone(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false})
	applied := func() (n int) {
		for _, c := range h.Cmds() {
			if c.String() == "nft -f -" {
				n++
			}
		}
		return n
	}
	before := applied()
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]bool{"open": false}); code != http.StatusOK {
		t.Errorf("closing again: %d", code)
	}
	if applied() != before {
		t.Error("closing a closed listener applied the table again")
	}
}

// At boot the engines' bridges aren't in the kernel yet: the restored table already holds the saved ones, so each is an
// app bridge from the moment dockerd makes it, never a local virtual network until the next check. A saved name the
// kernel shows as something other than a bridge is left out (TestAHungDockerKeepsItsBridges).
func TestTheSavedBridgesWaitForTheKernelAtBoot(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	h.Engines(map[string]string{"0123456789abcdef": "docker0"})
	call(t, sock, "POST", "/v1/firewall/check", nil)

	h2 := agenttest.NewHost(t.TempDir())
	h2.Engines(map[string]string{"0123456789abcdef": "docker0"})
	h2.DockerState("activating", "loaded")
	h2.Absent("docker0") // dockerd hasn't made it yet
	sock2, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h2.Run, Root: h2.Root, IsBridge: h2.IsBridge, HasInterface: h2.HasInterface})
	if table, _ := h2.Table(); !strings.Contains(table, `iifname { "docker0" } meta iifkind "bridge" jump apps_in`) {
		t.Errorf("the restored table doesn't hold the saved bridge the kernel will have:\n%s", table)
	}
	call(t, sock2, "POST", "/v1/firewall/check", nil)
	h2.ABridgeAgain("docker0") // dockerd made it, still activating
	call(t, sock2, "POST", "/v1/firewall/check", nil)
	if table, _ := h2.Table(); !strings.Contains(table, `iifname { "docker0" } meta iifkind "bridge" jump apps_in`) {
		t.Errorf("the saved bridge was forgotten before the kernel had it:\n%s", table)
	}
}

func TestALostStateIsKeptWhenTheFirstSaveFails(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "agent.json"), []byte(`{"enforc`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := agenttest.NewHost(t.TempDir())
	sock, _ := agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root})
	// The save can't write its new file (a folder where atomicfile's temporary one goes, as a full disk would).
	if err := os.Mkdir(filepath.Join(state, "agent.json.new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, sock, "PUT", "/v1/firewall/setup-listener", map[string]any{"open": true, "port": 7444}); code != http.StatusInternalServerError {
		t.Errorf("a save that failed: %d, want 500", code)
	}
	if b, err := os.ReadFile(filepath.Join(state, "agent.json")); err != nil || string(b) != `{"enforc` {
		t.Errorf("after the failed save the state file is %q, %v: the loss would be forgotten", b, err)
	}
	if s := status(t, sock); !s.StateUnreadable {
		t.Errorf("after the failed save: %+v", s)
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

// A full-tunnel VPN's interface is a way out for apps, not the LAN (#108).
func TestTheTableLetsAppsOutThroughTheUplinks(t *testing.T) {
	testenv.Isolate(t)
	network := func() (lan.Network, error) {
		return lan.Network{LAN: []string{"enp5s0"}, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
			Uplinks4: []string{"enp5s0", "wg0"}, Uplinks6: []string{"enp5s0"}}, nil
	}
	sock, h := agenttest.StartWith(t, agent.Config{Network: network})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "POST", "/v1/firewall/check", nil)
	table, _ := h.Table()
	if !strings.Contains(table, `meta nfproto ipv4 oifname != { "enp5s0", "wg0" } drop`) ||
		!strings.Contains(table, `meta nfproto ipv6 oifname != { "enp5s0" } drop`) {
		t.Errorf("apps can't leave through the VPN's tunnel, or leave over IPv6 through it, which it doesn't cover:\n%s", table)
	}
	if !strings.Contains(table, `iifname { "enp5s0" } tcp dport 7020 accept`) || strings.Contains(table, `"wg0" } tcp dport`) {
		t.Errorf("the setup listener isn't the LAN's alone:\n%s", table)
	}
}

// While the kernel can't be asked (#148), the table keeps the last reading of the network: the LAN's prefixes stay out
// of the apps' reach, and its uplinks keep their rules. An agent that never read the network has no LAN,
// no way out for apps and no setup listener open: it fails closed. It follows the network again once the kernel
// answers.
func TestWhileTheKernelCantBeAskedTheTableKeepsTheLastNetwork(t *testing.T) {
	testenv.Isolate(t)
	var mu sync.Mutex
	var failing error
	read := lan.Network{LAN: []string{"enp5s0"}, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("2001:db8:1::/64")}, Uplinks4: []string{"enp5s0", "wg0"}, Uplinks6: []string{"enp5s0"}}
	network := func() (lan.Network, error) {
		mu.Lock()
		defer mu.Unlock()
		if failing != nil {
			return lan.Network{}, failing
		}
		return read, nil
	}
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		failing = err
	}
	sock, h := agenttest.StartWith(t, agent.Config{Network: network})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "POST", "/v1/firewall/check", nil)
	before, _ := h.Table()
	for _, want := range []string{"2001:db8:1::/64", `meta nfproto ipv4 oifname != { "enp5s0", "wg0" } drop`, `iifname { "enp5s0", "wg0" } drop`} {
		if !strings.Contains(before, want) {
			t.Fatalf("the table lacks %q:\n%s", want, before)
		}
	}
	fail(errors.New("netlink: the dump was interrupted by a change"))
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if after, _ := h.Table(); after != before {
		t.Errorf("while the kernel can't be asked, the table changed:\n%s\nwant the last reading's:\n%s", after, before)
	}
	fail(nil)
	read.Prefixes = append(read.Prefixes, netip.MustParsePrefix("2001:db8:5::/64"))
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if after, _ := h.Table(); !strings.Contains(after, "2001:db8:5::/64") {
		t.Errorf("the table didn't follow the network once the kernel answered:\n%s", after)
	}

	// Never read: closed.
	never := func() (lan.Network, error) { return lan.Network{}, errors.New("netlink: no answer") }
	sock, h = agenttest.StartWith(t, agent.Config{Network: never})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	call(t, sock, "POST", "/v1/firewall/check", nil)
	table, _ := h.Table()
	_, outbound, _ := strings.Cut(table, "\tchain outbound {\n")
	if !strings.HasPrefix(outbound, "\t\tdrop\n") || strings.Contains(table, "tcp dport 7020") || strings.Contains(table, "jump virtual_in") {
		t.Errorf("an agent that never read the network, want no way out for apps, no setup listener and no local "+
			"virtual network:\n%s", table)
	}
}

// NetBird's drop-in and a sealed credential are on the disk before they count, as the agent's state is: the file's
// data, then the folder's entry. After a power cut neither is empty (#133).
func TestTheDropInAndASealedCredentialAreSyncedBeforeTheyCount(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	var mu sync.Mutex
	var synced []string
	defer atomicfile.SetSync(func(f *os.File) error {
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		kind := "file"
		if fi.IsDir() {
			kind = "folder"
		}
		synced = append(synced, kind+" "+f.Name())
		return nil
	})()
	sock, h := agenttest.StartWith(t, agent.Config{StateDir: state})
	call(t, sock, "POST", "/v1/mesh/join", joinBody())
	dropIn := filepath.Join(h.Root, "etc/systemd/system/netbird.service.d", "10-homerslab.conf")
	if fi, err := os.Stat(dropIn); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("the drop-in: %v, %v; want mode 644", fi, err)
	}
	if !slices.Contains(synced, "file "+dropIn+".new") || !slices.Contains(synced, "folder "+filepath.Dir(dropIn)) {
		t.Errorf("the drop-in's file and folder weren't both synced: %v", synced)
	}

	synced = nil
	code, body := call(t, sock, "PUT", "/v1/credentials/manager-mesh-token", map[string]string{"value": "nbp_0123456789abcdefghijklmnopqrstuvwxyzAB"})
	if code != http.StatusNoContent {
		t.Fatalf("sealing: %d %s", code, body)
	}
	cred := filepath.Join(state, "credentials", "manager-mesh-token.cred")
	if !slices.Contains(synced, "file "+cred+".new") || !slices.Contains(synced, "folder "+filepath.Dir(cred)) {
		t.Errorf("the sealed credential's file and folder weren't both synced: %v", synced)
	}
}

// appNetworks are the bridges a table takes for app networks: its apps_in and apps_out jumps and apps_out's own
// bridges' rules.
func appNetworks(table string) string {
	var out []string
	for _, l := range strings.Split(table, "\n") {
		if strings.Contains(l, "jump apps_") || strings.Contains(l, "oifname \"") && strings.Contains(l, "iifname \"") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return strings.Join(out, "\n")
}
