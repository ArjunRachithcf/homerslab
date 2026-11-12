package lan_test

// The LAN as the real kernel reports it, in a throwaway network namespace, never the machine's own: the test runs
// itself again under `unshare -rn` (a user and network namespace; `unshare -n` as root, as in CI), and refuses to
// touch a namespace that holds more than loopback. As the firewall's tests do.

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
//   - tun2, a tunnel with one half only, in a table of its own: not an uplink;
//   - tun3, an IPv6-only tunnel (::/1 and 8000::/1) with one public IPv4 range split through it: an uplink for IPv6
//     only (#169);
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

		ip tuntap add mode tun name tun3
		ip link set tun3 up
		ip -6 addr add fd33::2/64 dev tun3 nodad
		ip -6 route add ::/1 dev tun3
		ip -6 route add 8000::/1 dev tun3
		ip route add 198.51.100.0/24 dev tun3

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

	l := asNICs(t, "eth0").LAN()
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

	n := asNICs(t, "eth0").Network()
	if want := []string{"eth0"}; !slices.Equal(n.LAN, want) {
		t.Errorf("the network's LAN = %v, want %v", n.LAN, want)
	}
	if want := append(slices.Clone(wantPrefixes), netip.MustParsePrefix("fd10:abcd:0:1::/64")); !sameSet(n.Prefixes, want) {
		t.Errorf("the network's prefixes = %v, want %v: every network on the LAN's link, which apps never reach", n.Prefixes, want)
	}
	if got, want := n.Uplinks4, []string{"eth0", "wg1", "tun0", "tun1"}; !sameSet(got, want) || got[0] != "eth0" {
		t.Errorf("IPv4 uplinks = %v, want %v, the LAN's first: apps reach the internet through the VPNs' tunnels too "+
			"(OpenVPN's def1 halves on tun1), not the dummy wg0, which is no tunnel, nor tun2, which has one half, nor "+
			"the IPv6-only tun3", got, want)
	}
	if got, want := n.Uplinks6, []string{"eth0", "tun0", "tun3"}; !sameSet(got, want) || got[0] != "eth0" {
		t.Errorf("IPv6 uplinks = %v, want %v: the LAN's, tun0's default route and tun3's halves, not the IPv4-only "+
			"tunnels", got, want)
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
	if n := readNetwork(t); len(n.Uplinks4) != 0 || len(n.Uplinks6) != 0 {
		t.Errorf("uplinks = %v and %v, want none: wg0 is a dummy, not a tunnel", n.Uplinks4, n.Uplinks6)
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
	k := asNICs(t, "eth0")
	if n := k.Network(); !slices.Equal(n.Uplinks4, []string{"eth0"}) || len(n.Uplinks6) != 0 {
		t.Errorf("uplinks = %v and %v, want eth0 for IPv4 only: the bridge is a local virtual network", n.Uplinks4, n.Uplinks6)
	}
	if got := k.Interfaces(); !slices.Equal(got, []string{"eth0"}) {
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
	l := asNICs(t, "eth0", "eth1").LAN()
	if want := []string{"eth0", "eth1"}; !sameSet(l.Interfaces, want) {
		t.Errorf("interfaces = %v, want %v", l.Interfaces, want)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("192.168.2.0/24"),
		netip.MustParsePrefix("fd00:2::/64")}
	if !sameSet(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
}

// Only what may be a home network is the LAN, as the real kernel describes its links (#169): a NIC, a bond of NICs
// and a VLAN on it, and a VM host's bridge (a NIC, a container's veth and a VM's tap). Not a bridge with an L2 tunnel
// among its ports, a bridge of veths, a veth, nor a tunnel of the kinds the kernel here can make (bareudp, ERSPAN, AMT,
// gretap), each with a default route in the main table. Only a bridge of guests' ports, or of none, is a local virtual
// network.
func TestOnlyAHomeNetworkIsTheLANInANetworkNamespace(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv(insideNamespace) != "1" {
		reexecInNamespace(t, "^TestOnlyAHomeNetworkIsTheLANInANetworkNamespace$")
		return
	}
	freshNamespace(t)
	sh(t, `
		ip link set lo up
		for i in eth0 eth1 eth2 eth3 eth4; do ip link add $i type dummy; done
		ip link add bond0 type bond
		ip link set eth1 master bond0
		ip link set eth2 master bond0
		ip link add link bond0 name bond0.20 type vlan id 20
		ip link add br0 type bridge
		ip link set eth3 master br0
		ip link add veth-a type veth peer name veth-b
		ip link set veth-a master br0
		ip tuntap add mode tap name tap0
		ip link set tap0 master br0
		ip link add br1 type bridge
		ip link set eth4 master br1
		ip link add vx0 type vxlan id 5 dstport 4789 local 192.168.1.20
		ip link set vx0 master br1
		ip link add br2 type bridge
		ip link add veth-c type veth peer name veth-d
		ip link set veth-c master br2
		ip link add link veth-b name veth-b.7 type vlan id 7
		ip link add br3 type bridge
		ip tuntap add mode tap name tap3
		ip link set tap3 master br3
		ip link add br4 type bridge
		for i in eth0 eth1 eth2 eth3 eth4 bond0 bond0.20 br0 veth-a veth-b tap0 br1 vx0 br2 veth-c veth-d veth-b.7; do
			ip link set $i up
		done
		ip addr add 192.168.1.20/24 dev eth0
		m=10
		for i in eth0 bond0 bond0.20 br0 br1 br2 veth-b veth-b.7; do ip route add default dev $i metric $m; m=$((m+1)); done
	`)
	// A VLAN whose link is in another namespace, at the index eth0 has here: the kernel's IFLA_LINK names that other
	// namespace's link, never eth0.
	other := exec.Command("unshare", "-n", "sleep", "300")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	time.Sleep(100 * time.Millisecond)
	eth0 := slices.IndexFunc(asNICs(t).Links, func(l lan.Link) bool { return l.Name == "eth0" })
	sh(t, fmt.Sprintf(`nsenter -t %d -n sh -ec "ip link add far index %d type dummy; ip link set far up
			ip link add link far name v30 type vlan id 30; ip link set v30 netns %d"
		ip link set v30 up
		ip route add default dev v30 metric 30`, other.Process.Pid, asNICs(t).Links[eth0].Index, os.Getpid()))
	// Tunnels, each of a kind whose module this kernel may lack (bareudp and AMT are newer): those it can make.
	var tunnels []string
	for i, c := range []string{
		"bu0 type bareudp dstport 6635 ethertype mpls_uc",
		"er0 type erspan seq key 10 erspan_ver 1 erspan 123 local 192.168.1.20 remote 192.0.2.2",
		"amt0 type amt mode gateway local 192.168.1.20 discovery 192.0.2.3 dev eth0",
		"gt0 type gretap local 192.168.1.20 remote 192.0.2.4",
	} {
		name, _, _ := strings.Cut(c, " ")
		if out, err := exec.Command("sh", "-c", "ip link add "+c).CombinedOutput(); err != nil {
			t.Logf("no %s here: %s", name, bytes.TrimSpace(out))
			continue
		}
		sh(t, "ip link set "+name+" up; ip route add default dev "+name+" metric "+strconv.Itoa(100+i))
		tunnels = append(tunnels, name)
	}
	t.Logf("tunnels with the default route: %v", tunnels)

	k := asNICs(t, "eth0", "eth1", "eth2", "eth3", "eth4")
	link := func(name string) lan.Link {
		i := slices.IndexFunc(k.Links, func(l lan.Link) bool { return l.Name == name })
		if i < 0 {
			t.Fatalf("no link %s", name)
		}
		return k.Links[i]
	}
	for port, master := range map[string]string{"eth1": "bond0", "eth2": "bond0", "eth3": "br0", "veth-a": "br0",
		"tap0": "br0", "eth4": "br1", "vx0": "br1", "veth-c": "br2", "eth0": "", "bond0": "", "veth-b": ""} {
		if got, want := link(port).Master, 0; master != "" {
			want = link(master).Index
			if got != want {
				t.Errorf("%s's master = %d, want %s's %d", port, got, master, want)
			}
		} else if got != want {
			t.Errorf("%s's master = %d, want none", port, got)
		}
	}
	if got, want := link("bond0.20").Lower, link("bond0").Index; got != want {
		t.Errorf("bond0.20 sits on %d, want bond0's %d", got, want)
	}
	if got := link("eth0").Lower; got != 0 {
		t.Errorf("eth0 sits on %d, want nothing", got)
	}
	if got := link("v30").Lower; got != 0 {
		t.Errorf("v30 sits on %d here, want nothing: its link is in another namespace", got)
	}
	want := []string{"eth0", "bond0", "bond0.20", "br0"}
	if got := k.Interfaces(); !slices.Equal(got, want) {
		t.Errorf("interfaces = %v, want %v: not br1 (a VXLAN port), br2 (veths only), veth-b, a VLAN on it, one on another "+
			"namespace's link, or the tunnels %v", got, want, tunnels)
	}
	if n := k.Network(); !slices.Equal(n.LAN, want) {
		t.Errorf("the network's LAN = %v, want %v", n.LAN, want)
	}
	// The local virtual networks: br2 (a container's veth), br3 (a VM's tap) and br4 (no port yet); not br0 or br1,
	// which have a NIC among their ports.
	if got, want := k.Network().Virtual, []string{"br2", "br3", "br4"}; !slices.Equal(got, want) {
		t.Errorf("local virtual networks = %v, want %v", got, want)
	}
}

// asNICs is the kernel as this namespace reports it, with the dummies nics read as the physical NICs they stand for:
// a namespace can hold no NIC, and the kernel names no kind for a NIC.
func asNICs(t *testing.T, nics ...string) lan.Kernel {
	t.Helper()
	k, err := lan.ReadKernel()
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range k.Links {
		if slices.Contains(nics, l.Name) {
			if l.Kind != "dummy" {
				t.Fatalf("%s is a %q, not the dummy that stands for a NIC", l.Name, l.Kind)
			}
			k.Links[i].Kind = ""
		}
	}
	return k
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
	addrs, err := exec.Command("ip", "-o", "addr", "show").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !fresh(string(links), string(addrs)) {
		t.Fatalf("refusing: this network namespace isn't fresh:\n%s", links)
	}
}

// fresh says whether `ip -o link show` and `ip -o addr show` list a brand-new network namespace: loopback, and at most
// the fallback tunnel devices the kernel adds to every namespace once their module is loaded (as a test that makes an
// erspan or a gretap link loads ip_gre), by their exact names, down and with no address. Never the machine's own
// namespace, which has its NICs.
func fresh(links, addrs string) bool {
	for _, l := range strings.Split(strings.TrimSpace(addrs), "\n") {
		if f := strings.Fields(l); len(f) > 0 && (len(f) < 2 || f[1] != "lo") {
			return false
		}
	}
	fallback := []string{"gre0", "gretap0", "erspan0", "tunl0", "sit0", "ip6tnl0", "ip6gre0", "ip_vti0", "ip6_vti0"}
	lines := strings.Split(strings.TrimSpace(links), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 {
			return false
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		up := slices.Contains(strings.Split(strings.Trim(f[2], "<>"), ","), "UP")
		if i == 0 && name != "lo" || i > 0 && (!slices.Contains(fallback, name) || up) {
			return false
		}
	}
	return len(lines) > 0
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
