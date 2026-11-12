package lan_test

// The LAN as the real kernel reports it, in a throwaway network namespace, never the machine's own: the test runs
// itself again under `unshare -rn` (a user and network namespace; `unshare -n` as root, as in CI), and refuses to
// touch a namespace that holds more than loopback. As the firewall's tests do.

import (
	"bytes"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const insideNamespace = "HOMERSLAB_LAN_NETNS"

// The machine in the namespace:
//   - eth0, the home network: 192.168.1.20/24 and the default route through 192.168.1.1; a ULA and a global /64
//     from the router, and the IPv6 default route through it;
//   - wg0, a full-tunnel VPN as wg-quick makes one: its default routes in table 51820 (and a rule sending everything
//     there), never in main. A dummy interface, so only the table tells;
//   - wg1, a real WireGuard full tunnel in table 51821: not the LAN, but an uplink;
//   - wt0, the mesh: a real WireGuard interface with its CGNAT address and its ULA /64;
//   - tun0, a VPN client's tun device that puts a default route in the main table, behind eth0's;
//   - tun1, OpenVPN's def1: 0.0.0.0/1 and 128.0.0.0/1 through it instead of a default route, an uplink too (#148);
//   - tun2, a tunnel with one half only, in a table of its own: a tunnel, not an uplink;
//   - and a rogue router advertisement on the LAN: the mesh's ULA /64 on eth0, with an address from it.
func TestTheLANInANetworkNamespace(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespace) != "1" {
		reexecInNamespace(t, "^TestTheLANInANetworkNamespace$")
		return
	}
	freshNamespace(t)
	sh(t, `
		ip link set lo up
		ip link add eth0 type dummy
		ip link set eth0 up
		ip addr add 192.168.1.20/24 dev eth0
		ip route add default via 192.168.1.1 dev eth0
		ip -6 addr add fd00:1::20/64 dev eth0 nodad
		ip -6 addr add 2001:db8:1::20/64 dev eth0 nodad
		ip -6 route add default via fe80::1 dev eth0 metric 100

		ip link add wg0 type dummy
		ip link set wg0 up
		ip addr add 10.2.0.2/32 dev wg0
		ip -6 addr add fd42:42:42::2/128 dev wg0 nodad
		ip route add default dev wg0 table 51820
		ip -6 route add default dev wg0 table 51820
		ip -6 route add 2001:db8:5::/64 dev wg0 table 51820
		ip rule add not fwmark 51820 table 51820
		ip -6 rule add not fwmark 51820 table 51820

		ip link add wg1 type wireguard
		ip link set wg1 up
		ip addr add 10.66.0.2/32 dev wg1
		ip route add default dev wg1 table 51821

		ip link add wt0 type wireguard
		ip link set wt0 up
		ip addr add 100.70.1.2/16 dev wt0
		ip -6 addr add fd10:abcd:0:1::2/64 dev wt0 nodad

		ip tuntap add mode tun name tun0
		ip link set tun0 up
		ip addr add 10.8.0.2/24 dev tun0
		ip addr add 10.8.1.1/32 peer 10.8.9.0/24 dev tun0
		ip route add default dev tun0 metric 500
		ip -6 route add default dev tun0 metric 500

		ip tuntap add mode tun name tun1
		ip link set tun1 up
		ip addr add 10.9.0.2/24 dev tun1
		ip route add 0.0.0.0/1 via 10.9.0.1 dev tun1
		ip route add 128.0.0.0/1 via 10.9.0.1 dev tun1

		ip tuntap add mode tun name tun2
		ip link set tun2 up
		ip addr add 10.10.0.2/24 dev tun2
		ip route add 0.0.0.0/1 dev tun2 table 100

		ip -6 addr add fd10:abcd:0:1::99/64 dev eth0 nodad
	`)

	k, err := lan.ReadKernel()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]lan.Link{
		"lo": {Name: "lo", Loopback: true}, "eth0": {Name: "eth0", Kind: "dummy"}, "wg0": {Name: "wg0", Kind: "dummy"},
		"wt0": {Name: "wt0", Kind: "wireguard", PointToPoint: true}, "tun0": {Name: "tun0", Kind: "tun", PointToPoint: true},
	} {
		i := slices.IndexFunc(k.Links, func(l lan.Link) bool { return l.Name == name })
		if i < 0 {
			t.Errorf("no link %s in %+v", name, k.Links)
			continue
		}
		if got := k.Links[i]; got.Index == 0 || got.Name != want.Name || got.Kind != want.Kind ||
			got.PointToPoint != want.PointToPoint || got.Loopback != want.Loopback {
			t.Errorf("link %s = %+v, want %+v", name, got, want)
		}
	}
	tun0 := k.Links[slices.IndexFunc(k.Links, func(l lan.Link) bool { return l.Name == "tun0" })].Index
	var onTun0 []lan.Addr
	for _, a := range k.Addrs {
		if a.Index == tun0 && a.Prefix.Addr().Is4() {
			a.Flags = 0
			onTun0 = append(onTun0, a)
		}
	}
	if want := []lan.Addr{{Index: tun0, Prefix: netip.MustParsePrefix("10.8.0.2/24")},
		{Index: tun0, Prefix: netip.MustParsePrefix("10.8.1.1/32"), Peer: netip.MustParsePrefix("10.8.9.0/24")}}; !sameSet(onTun0, want) {
		t.Errorf("tun0's addresses = %v, want %v: the machine's own end of a point-to-point link, and its peer's network", onTun0, want)
	}

	l := lan.Current()
	if want := []string{"eth0"}; !slices.Equal(l.Interfaces, want) {
		t.Errorf("interfaces = %v, want %v: not the full-tunnel VPN's wg0, the mesh's wt0 or tun0", l.Interfaces, want)
	}
	wantPrefixes := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fd00:1::/64"),
		netip.MustParsePrefix("2001:db8:1::/64")}
	if !sameSet(l.Prefixes, wantPrefixes) {
		t.Errorf("prefixes = %v, want %v: not the mesh's /64 a rogue router advertised", l.Prefixes, wantPrefixes)
	}
	wantAddrs := []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fd00:1::20"),
		netip.MustParseAddr("2001:db8:1::20")}
	if !sameSet(l.Addresses, wantAddrs) {
		t.Errorf("addresses = %v, want %v", l.Addresses, wantAddrs)
	}
	for addr, want := range map[string]bool{
		"192.168.1.42": true, "fd00:1::42": true, "2001:db8:1::42": true,
		"fd10:abcd:0:1::3": false, "100.70.1.3": false, "10.2.0.9": false, "fd42:42:42::9": false, "10.8.0.9": false,
		"2001:db8:5::1": false, "fe80::1%wg0": false, "fe80::1%wt0": false,
	} {
		if got := lan.FromHere(netip.MustParseAddr(addr), l); got != want {
			t.Errorf("FromHere(%s) = %v, want %v", addr, got, want)
		}
	}

	n := readNetwork(t)
	if want := []string{"eth0"}; !slices.Equal(n.LAN, want) {
		t.Errorf("the network's LAN = %v, want %v", n.LAN, want)
	}
	if want := append(slices.Clone(wantPrefixes), netip.MustParsePrefix("fd10:abcd:0:1::/64")); !sameSet(n.Prefixes, want) {
		t.Errorf("the network's prefixes = %v, want %v: every network on the LAN's link, which apps never reach", n.Prefixes, want)
	}
	if got, want := n.Uplinks, []string{"eth0", "wg1", "tun0", "tun1"}; !sameSet(got, want) || got[0] != "eth0" {
		t.Errorf("uplinks = %v, want %v, the LAN's first: apps reach the internet through the VPNs' tunnels too (OpenVPN's "+
			"def1 halves on tun1), not the dummy wg0, which is no tunnel, nor tun2, which has one half", got, want)
	}
	if got, want := n.Tunnels, []string{"wg1", "wt0", "tun0", "tun1", "tun2"}; !sameSet(got, want) {
		t.Errorf("tunnels = %v, want %v: every tunnel, with a default route or without", got, want)
	}
}

// With no default route in the main table, there is no LAN, whatever the other tables hold.
func TestNoLANWhenOnlyAnotherTableHasADefaultRoute(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespace) != "1" {
		reexecInNamespace(t, "^TestNoLANWhenOnlyAnotherTableHasADefaultRoute$")
		return
	}
	freshNamespace(t)
	sh(t, `
		ip link set lo up
		ip link add wg0 type dummy
		ip link set wg0 up
		ip addr add 192.168.9.2/24 dev wg0
		ip -6 addr add fd00:9::2/64 dev wg0 nodad
		ip route add default via 192.168.9.1 dev wg0 table 51820
		ip -6 route add default via fe80::1 dev wg0 table 51820
	`)
	if l := lan.Current(); len(l.Interfaces) != 0 || len(l.Prefixes) != 0 || len(l.Addresses) != 0 {
		t.Errorf("LAN = %+v, want none: the default routes are in table 51820 only", l)
	}
	if n := readNetwork(t); len(n.LAN) != 0 || len(n.Prefixes) != 0 {
		t.Errorf("the network's LAN = %v %v, want none", n.LAN, n.Prefixes)
	}
	if got := readNetwork(t).Uplinks; len(got) != 0 {
		t.Errorf("uplinks = %v, want none: wg0 is a dummy, not a tunnel", got)
	}
}

// A local virtual network's bridge with a default route in a table of its own (policy routing for its VMs) is
// neither the LAN nor an uplink: only the LAN's interfaces and tunnels are, or its VMs lose the internet the firewall
// forwards them (#108's review).
func TestABridgeWithADefaultRouteInAnotherTableIsNoUplink(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespace) != "1" {
		reexecInNamespace(t, "^TestABridgeWithADefaultRouteInAnotherTableIsNoUplink$")
		return
	}
	freshNamespace(t)
	sh(t, `
		ip link set lo up
		ip link add eth0 type dummy
		ip link set eth0 up
		ip addr add 192.168.1.20/24 dev eth0
		ip route add default via 192.168.1.1 dev eth0
		ip link add virbr0 type bridge
		ip link set virbr0 up
		ip addr add 192.168.122.1/24 dev virbr0
		ip route add default via 192.168.122.254 dev virbr0 table 100
		ip -6 addr add fd00:7a::1/64 dev virbr0 nodad
		ip -6 route add default via fd00:7a::fe dev virbr0 table 100
	`)
	if got := readNetwork(t).Uplinks; !slices.Equal(got, []string{"eth0"}) {
		t.Errorf("uplinks = %v, want eth0 only: the bridge is a local virtual network", got)
	}
	if got := lan.Current().Interfaces; !slices.Equal(got, []string{"eth0"}) {
		t.Errorf("interfaces = %v, want eth0 only", got)
	}
}

// A default route over two links at once (multipath) makes both the LAN; its next hops through a tunnel don't.
func TestAMultipathDefaultRouteInANetworkNamespace(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespace) != "1" {
		reexecInNamespace(t, "^TestAMultipathDefaultRouteInANetworkNamespace$")
		return
	}
	freshNamespace(t)
	sh(t, `
		ip link set lo up
		for i in eth0 eth1; do ip link add $i type dummy; ip link set $i up; done
		ip link add wt0 type wireguard
		ip link set wt0 up
		ip addr add 192.168.1.20/24 dev eth0
		ip addr add 192.168.2.20/24 dev eth1
		ip addr add 100.70.1.2/16 dev wt0
		ip route add default nexthop via 192.168.1.1 dev eth0 nexthop via 192.168.2.1 dev eth1 nexthop dev wt0
		ip -6 addr add fd00:2::20/64 dev eth1 nodad
		ip -6 route add default nexthop via fe80::1 dev eth1 nexthop via fe80::2 dev eth0
	`)
	l := lan.Current()
	if want := []string{"eth0", "eth1"}; !sameSet(l.Interfaces, want) {
		t.Errorf("interfaces = %v, want %v", l.Interfaces, want)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("192.168.2.0/24"),
		netip.MustParsePrefix("fd00:2::/64")}
	if !sameSet(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
}

func readNetwork(t *testing.T) lan.Network {
	t.Helper()
	n, err := lan.ReadNetwork()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func sh(t *testing.T, script string) {
	t.Helper()
	if out, err := exec.Command("sh", "-ec", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s\n%s", err, script, out)
	}
}

// freshNamespace refuses to go on unless this network namespace holds loopback only: never the machine's own.
func freshNamespace(t *testing.T) {
	t.Helper()
	links, err := exec.Command("ip", "-o", "link", "show").Output()
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(links)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], ": lo:") {
		t.Fatalf("refusing: this network namespace isn't fresh (%d links)", len(lines))
	}
}

// reexecInNamespace runs the test again in its own user and network namespace.
func reexecInNamespace(t *testing.T, run string) {
	t.Helper()
	for _, tool := range []string{"unshare", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("HOMERSLAB_REQUIRE_NETNS") == "1" {
				t.Fatalf("%s isn't installed", tool)
			}
			t.Skipf("%s isn't installed: the LAN isn't tested in a namespace", tool)
		}
	}
	flags := "-rn"
	if os.Geteuid() == 0 {
		flags = "-n"
	}
	cmd := exec.Command("unshare", flags, os.Args[0], "-test.run="+run, "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), insideNamespace+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("unshare failed")) && os.Getenv("HOMERSLAB_REQUIRE_NETNS") != "1" {
			t.Skipf("no user namespaces here: %s", bytes.TrimSpace(out))
		}
		t.Fatalf("in a namespace: %v\n%s", err, out)
	}
	t.Logf("in a namespace:\n%s", out)
}
