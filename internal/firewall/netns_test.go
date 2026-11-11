package firewall_test

// The firewall table with the real nft, in throwaway network namespaces, never the machine's own: the test runs
// itself again under `unshare -rn` (a user and network namespace; `unshare -n` as root, as in CI), and refuses to
// touch any namespace that holds more than loopback. Prior art: classic's tests/firewall/guard.sh.
//
// The box (this process) has lan0 to a LAN host, wt0 to a mesh peer, and a container bridge br-test with one
// container behind Docker-style DNAT (a published port). The LAN host routes the box's mesh address and the
// container's network through the box, as a hostile device at home would.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const (
	insideNamespaces = "HOMERSLAB_FIREWALL_NETNS"
	helper           = "HOMERSLAB_FIREWALL_HELPER" // listen or dial, in a child namespace
)

func TestMain(m *testing.M) {
	switch os.Getenv(helper) {
	case "listen":
		ln, err := net.Listen("tcp", os.Args[len(os.Args)-1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("listening")
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(1)
			}
			_, _ = c.Write([]byte("hello\n"))
			c.Close()
		}
	case "dial":
		c, err := net.DialTimeout("tcp", os.Args[len(os.Args)-1], 700*time.Millisecond)
		if err != nil {
			os.Exit(1)
		}
		_ = c.SetDeadline(time.Now().Add(700 * time.Millisecond))
		b, _ := io.ReadAll(c)
		if string(b) != "hello\n" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestTheFirewallTableInNetworkNamespaces(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespaces) != "1" {
		reexec(t)
		return
	}
	n := newNet(t)

	// The holes the table closes, so the checks below can tell.
	n.want(t, "before the table: the LAN reaches a listener on the box", "lan", "192.168.77.1:8022", true)
	n.want(t, "before the table: the LAN host's route through the box reaches the container", "lan", "172.30.0.2:8444", true)
	n.want(t, "before the table: the LAN reaches the published port", "lan", "192.168.77.1:8444", true)

	fw := &firewall.Firewall{Run: host.Exec}
	ctx := context.Background()
	open := firewall.State{Mesh: []string{"wt0"}, WireGuard: []int{51820}, SetupPort: 7020}
	if err := fw.Apply(ctx, open); err != nil {
		t.Fatal(err)
	}
	lanKeptOut := func(when string) {
		n.want(t, when+": the LAN reaches no listener on the box", "lan", "192.168.77.1:8022", false)
		n.want(t, when+": the LAN reaches no listener at the box's mesh address", "lan", "100.88.1.1:8022", false)
		n.want(t, when+": the LAN reaches no listener over IPv6", "lan", "[fd00:77::1]:8022", false)
		n.want(t, when+": no published port answers the LAN", "lan", "192.168.77.1:8444", false)
		n.want(t, when+": nothing from the LAN is routed to the containers", "lan", "172.30.0.2:8444", false)
		n.want(t, when+": nothing from the LAN is routed to the mesh", "lan", "100.88.1.2:8022", false)
	}
	lanKeptOut("with the table")
	n.want(t, "the setup link's LAN listener is the one exception", "lan", "192.168.77.1:7020", true)
	n.ping(t, "the LAN still reaches the machine itself (ping)", "192.168.77.1", true)
	n.want(t, "a mesh peer reaches the box", "peer", "100.88.1.1:8022", true)
	n.want(t, "a mesh peer reaches the published port", "peer", "100.88.1.1:8444", true)
	n.want(t, "a container reaches the box", "ctr", "172.30.0.1:8022", true)
	n.want(t, "a container reaches the LAN (its own connections)", "ctr", "192.168.77.50:9000", true)
	n.want(t, "the box reaches the LAN (its own connections)", "", "192.168.77.50:9000", true)

	if err := fw.Apply(ctx, firewall.State{Mesh: []string{"wt0"}, WireGuard: []int{51820}}); err != nil {
		t.Fatal(err)
	}
	n.want(t, "after the admin's first sign-in over the mesh, the LAN listener is closed", "lan", "192.168.77.1:7020", false)
	n.want(t, "and the mesh still reaches it", "peer", "100.88.1.1:7020", true)
	lanKeptOut("closed")

	for _, tamper := range []struct{ what, nft string }{
		{"changed", "flush chain inet homerslab input"},
		{"changed", "add rule inet homerslab forward accept"},
		{"missing", "delete table inet homerslab"},
	} {
		if _, err := host.Exec(ctx, host.Cmd{Name: "nft", Args: strings.Fields(tamper.nft)}); err != nil {
			t.Fatal(err)
		}
		d, err := fw.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if d == nil || d.What != tamper.what {
			t.Errorf("after %q the check found %+v, want %s", tamper.nft, d, tamper.what)
		}
		lanKeptOut("after " + tamper.nft + " and the check")
		if d, _ := fw.Check(ctx); d != nil {
			t.Errorf("a check right after the table was put back found drift: %+v", d)
		}
	}
	if got := len(fw.Status().Drift); got != 3 {
		t.Errorf("%d drift reports, want 3", got)
	}
}

// reexec runs this test again in its own user and network namespace.
func reexec(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"unshare", "nsenter", "ip", "nft", "ping"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("HOMERSLAB_REQUIRE_NETNS") == "1" {
				t.Fatalf("%s isn't installed", tool)
			}
			t.Skipf("%s isn't installed: the firewall table isn't tested in namespaces", tool)
		}
	}
	flags := "-rn"
	if os.Geteuid() == 0 {
		flags = "-n"
	}
	cmd := exec.Command("unshare", flags, os.Args[0], "-test.run=^TestTheFirewallTableInNetworkNamespaces$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), insideNamespaces+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("unshare failed")) && os.Getenv("HOMERSLAB_REQUIRE_NETNS") != "1" {
			t.Skipf("no user namespaces here: %s", bytes.TrimSpace(out))
		}
		t.Fatalf("in namespaces: %v\n%s", err, out)
	}
	t.Logf("in namespaces:\n%s", out)
}

type netns struct {
	pids map[string]int // child namespaces, each held by a sleep
}

func newNet(t *testing.T) *netns {
	t.Helper()
	// Never the machine's own network: this namespace must be brand new.
	links, err := exec.Command("ip", "-o", "link", "show").Output()
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(links)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], ": lo:") {
		t.Fatalf("refusing: this network namespace isn't fresh (%d links)", len(lines))
	}
	n := &netns{pids: map[string]int{}}
	for _, name := range []string{"lan", "peer", "ctr"} {
		c := exec.Command("unshare", "-n", "sleep", "300")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		n.pids[name] = c.Process.Pid
	}
	time.Sleep(200 * time.Millisecond)
	sh := func(script string) {
		t.Helper()
		if out, err := exec.Command("sh", "-ec", script).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s\n%s", err, script, out)
		}
	}
	in := func(name, script string) {
		t.Helper()
		sh(fmt.Sprintf("nsenter -t %d -n sh -ec '%s'", n.pids[name], script))
	}
	sh(fmt.Sprintf(`
		ip link set lo up
		ip link add lan0 type veth peer name eth0 netns %d
		ip link add wt0 type veth peer name eth0 netns %d
		ip link add br-test type bridge
		ip link add veth-ctr type veth peer name eth0 netns %d
		ip link set veth-ctr master br-test
		ip addr add 192.168.77.1/24 dev lan0
		ip -6 addr add fd00:77::1/64 dev lan0 nodad
		ip addr add 100.88.1.1/16 dev wt0
		ip addr add 172.30.0.1/24 dev br-test
		for i in lan0 wt0 br-test veth-ctr; do ip link set "$i" up; done
		echo 1 > /proc/sys/net/ipv4/ip_forward`, n.pids["lan"], n.pids["peer"], n.pids["ctr"]))
	in("lan", `ip link set lo up; ip link set eth0 up; ip addr add 192.168.77.50/24 dev eth0
		ip -6 addr add fd00:77::50/64 dev eth0 nodad
		ip route add 100.88.0.0/16 via 192.168.77.1; ip route add 172.30.0.0/24 via 192.168.77.1`)
	in("peer", `ip link set lo up; ip link set eth0 up; ip addr add 100.88.1.2/16 dev eth0; ip route add default via 100.88.1.1`)
	in("ctr", `ip link set lo up; ip link set eth0 up; ip addr add 172.30.0.2/24 dev eth0; ip route add default via 172.30.0.1`)
	// A published port, as Docker makes one: DNAT to the container, from outside and from the box itself.
	sh(`nft -f - <<'NFT'
table ip docker_like {
	chain prerouting { type nat hook prerouting priority dstnat; policy accept; tcp dport 8444 dnat to 172.30.0.2:8444; }
	chain output { type nat hook output priority -100; policy accept; ip daddr != 127.0.0.0/8 tcp dport 8444 dnat to 172.30.0.2:8444; }
}
NFT`)

	// Listeners: on the box for any address (an app, sshd), the setup listener, the container's app, and one on the
	// LAN host.
	for _, addr := range []string{":8022", ":7020"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte("hello\n"))
				c.Close()
			}
		}()
	}
	n.listen(t, "ctr", "172.30.0.2:8444")
	n.listen(t, "lan", "192.168.77.50:9000")
	return n
}

func (n *netns) helper(name, mode, addr string) *exec.Cmd {
	self, _ := os.Executable()
	var cmd *exec.Cmd
	if name == "" {
		cmd = exec.Command(self, addr)
	} else {
		cmd = exec.Command("nsenter", "-t", strconv.Itoa(n.pids[name]), "-n", self, addr)
	}
	cmd.Env = append(os.Environ(), helper+"="+mode)
	return cmd
}

func (n *netns) listen(t *testing.T, name, addr string) {
	t.Helper()
	cmd := n.helper(name, "listen", addr)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line := make([]byte, 10)
	if _, err := io.ReadFull(out, line); err != nil || string(line) != "listening\n" {
		t.Fatalf("no listener on %s in %s", addr, name)
	}
}

func (n *netns) want(t *testing.T, what, from, addr string, reach bool) {
	t.Helper()
	got := n.helper(from, "dial", addr).Run() == nil
	if got != reach {
		t.Errorf("%s: %s → %s connects %v, want %v", what, orBox(from), addr, got, reach)
	}
}

func (n *netns) ping(t *testing.T, what, addr string, reach bool) {
	t.Helper()
	got := exec.Command("nsenter", "-t", strconv.Itoa(n.pids["lan"]), "-n", "ping", "-c1", "-W1", addr).Run() == nil
	if got != reach {
		t.Errorf("%s: ping %s %v, want %v", what, addr, got, reach)
	}
}

func orBox(s string) string {
	if s == "" {
		return "box"
	}
	return s
}
