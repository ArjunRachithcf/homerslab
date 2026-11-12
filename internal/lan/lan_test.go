package lan_test

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestTheLANIsWhereTheDefaultRouteGoes(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().
		link("lo", "", lo).link("enp5s0", "", 0).link("docker0", "bridge", 0).link("wt0", "wireguard", ptp).link("wlp4s0", "", 0).
		route(mainTable, "0.0.0.0/0", gw, "enp5s0").
		route(mainTable, "192.168.1.0/24", onLink, "enp5s0").
		route(mainTable, "172.17.0.0/16", onLink, "docker0").
		route(mainTable, "100.64.0.0/16", onLink, "wt0").
		route(mainTable, "::/0", gw, "wlp4s0").
		route(mainTable, "fe80::/64", onLink, "enp5s0").
		route(mainTable, "::/0", onLink, "lo").k
	if got, want := k.Interfaces(), []string{"enp5s0", "wlp4s0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("interfaces = %v, want %v", got, want)
	}
	if got := (lan.Kernel{}).Interfaces(); got != nil {
		t.Errorf("with no routes: %v", got)
	}
}

// Only the main table's default routes count (#108): wg-quick puts a full tunnel's in table 51820, with a rule that
// sends everything there, and a policy route through another interface is no LAN either. Nor is a local, unreachable
// or blackhole default.
func TestOnlyTheMainTablesDefaultRouteMakesTheLAN(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().link("eth0", "", 0).link("wg0", "dummy", 0).link("eth1", "", 0).link("eth2", "", 0).
		addr("eth0", "192.168.1.20/24", 0).addr("wg0", "10.2.0.2/32", 0).addr("wg0", "fd42:42:42::2/64", 0).
		addr("eth1", "192.168.50.2/24", 0).addr("eth2", "10.9.0.2/24", 0).
		addr("eth2", "2001:db8:9::2/64", 0).
		route(mainTable, "0.0.0.0/0", gw, "eth0").
		route(51820, "0.0.0.0/0", onLink, "wg0").route(51820, "::/0", onLink, "wg0").
		route(51820, "fd42:42:42::/64", onLink, "wg0").
		route(100, "0.0.0.0/0", gw, "eth1").
		route(255, "::/0", onLink, "eth2").
		route(mainTable, "2001:db8:9::/64", onLink, "eth2").
		routeType(mainTable, "::/0", 7, "eth2").      // unreachable
		routeType(mainTable, "0.0.0.0/0", 6, "eth2"). // blackhole
		k
	l := k.LAN()
	if want := []string{"eth0"}; !reflect.DeepEqual(l.Interfaces, want) {
		t.Errorf("interfaces = %v, want %v", l.Interfaces, want)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}; !reflect.DeepEqual(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
	if ifaces, prefixes := k.OnLink(); !reflect.DeepEqual(ifaces, []string{"eth0"}) ||
		!reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}) {
		t.Errorf("OnLink = %v %v, want eth0 and its network only", ifaces, prefixes)
	}
}

// A VPN or the mesh is never the LAN, even with a default route in the main table: a tunnel by its kind of link, or
// any point-to-point link (#108).
func TestATunnelIsNeverTheLAN(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		kind  string
		flags linkFlags
	}{
		{"wireguard", ptp}, {"tun", ptp}, {"tun", 0}, {"ipip", 0}, {"gre", 0}, {"xfrm", 0}, {"ppp", ptp}, {"", ptp},
	} {
		k := kernel().link("eth0", "", 0).link("vpn0", c.kind, c.flags).
			addr("eth0", "192.168.1.20/24", 0).addr("vpn0", "10.8.0.2/24", 0).addr("vpn0", "fd00:8::2/64", 0).
			route(mainTable, "0.0.0.0/0", gw, "eth0").
			route(mainTable, "0.0.0.0/1", onLink, "vpn0").route(mainTable, "0.0.0.0/0", onLink, "vpn0").
			route(mainTable, "::/0", onLink, "vpn0").route(mainTable, "fd00:8::/64", onLink, "vpn0").k
		if got := k.Interfaces(); !reflect.DeepEqual(got, []string{"eth0"}) {
			t.Errorf("a %q link (point-to-point %v): interfaces = %v, want eth0 only", c.kind, c.flags == ptp, got)
		}
		if l := k.LAN(); lan.FromHere(netip.MustParseAddr("10.8.0.9"), l) || lan.FromHere(netip.MustParseAddr("fd00:8::9"), l) {
			t.Errorf("a %q link: its networks are the LAN: %v", c.kind, l.Prefixes)
		}
	}
}

// The mesh's ranges are never the LAN (#108): a prefix inside one is refused (a rogue router advertisement of the
// mesh's ULA /64 on the LAN; Tailscale's ULA), and a wider one keeps the rest of the network with the mesh's range
// carved out. An address inside one is never a LAN address. The firewall's list keeps them all: apps reach none.
func TestTheMeshsRangesAreNeverTheLAN(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().link("eth0", "", 0).link("wt0", "wireguard", ptp).
		addr("wt0", "100.70.1.2/16", 0).addr("wt0", "fd10:abcd:0:1::2/64", 0).addr("wt0", "fe80::2/64", 0).
		addr("eth0", "192.168.1.20/24", 0).
		addr("eth0", "fd00:1::20/64", 0).
		addr("eth0", "fd10:abcd:0:1::99/64", 0). // from the rogue advertisement
		addr("eth0", "fd10:abcd::99/48", 0).     // a wider network holding the mesh's
		addr("eth0", "fd10:abcd:0:1::77/48", 0). // in that network, but an address inside the mesh's range
		addr("eth0", "fd7a:115c:a1e0:ab12::5/64", 0).
		addr("eth0", "2001:db8:1::20/64", 0).
		addr("eth0", "100.70.9.9/24", 0).
		addr("eth0", "fe80::20/64", 0).
		route(mainTable, "0.0.0.0/0", gw, "eth0").route(mainTable, "::/0", gw, "eth0").
		route(mainTable, "fd10:abcd:0:1::/64", onLink, "eth0").route(mainTable, "2001:db8:1::/64", onLink, "eth0").
		route(mainTable, "fd10:abcd:0:1::/64", onLink, "wt0").k
	want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fd00:1::/64"),
		netip.MustParsePrefix("fd10:abcd::/48"), netip.MustParsePrefix("2001:db8:1::/64")}
	l := k.LAN()
	if !sameSet(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
	if wantAddrs := []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fd00:1::20"),
		netip.MustParseAddr("fd10:abcd::99"), netip.MustParseAddr("2001:db8:1::20")}; !reflect.DeepEqual(l.Addresses, wantAddrs) {
		t.Errorf("addresses = %v, want %v", l.Addresses, wantAddrs)
	}
	for a, wantHere := range map[string]bool{
		"fd10:abcd:0:1::3": false, "fd7a:115c:a1e0:ab12::9": false, "100.70.9.1": false, "100.70.1.3": false,
		"fd10:abcd::1": true, "fd10:abcd:0:2::1": true, "192.168.1.42": true,
	} {
		if got := lan.FromHere(netip.MustParseAddr(a), l); got != wantHere {
			t.Errorf("FromHere(%s) = %v, want %v", a, got, wantHere)
		}
	}
	wantOnLink := append(slices.Clone(want), netip.MustParsePrefix("fd10:abcd:0:1::/64"),
		netip.MustParsePrefix("fd7a:115c:a1e0:ab12::/64"), netip.MustParsePrefix("100.70.9.0/24"))
	if _, prefixes := k.OnLink(); !sameSet(prefixes, wantOnLink) {
		t.Errorf("OnLink prefixes = %v, want %v: apps are kept from every network on the LAN's links", prefixes, wantOnLink)
	}
}

// A VPN's address inside a wide home network (Proton VPN's 10.2.0.2/32 in a 10.0.0.0/8 LAN) carves that address
// out; the rest of the LAN stays the LAN. Its peer's network too.
func TestATunnelsAddressInsideTheLANIsCarvedOut(t *testing.T) {
	testenv.Isolate(t)
	b := kernel().link("eth0", "", 0).link("wg0", "wireguard", ptp).link("tun0", "tun", ptp).
		addr("eth0", "10.0.0.20/8", 0).addr("wg0", "10.2.0.2/32", 0).
		route(mainTable, "0.0.0.0/0", gw, "eth0").route(51820, "0.0.0.0/0", onLink, "wg0")
	b.k.Addrs = append(b.k.Addrs, lan.Addr{Index: b.index("tun0"), Prefix: netip.MustParsePrefix("10.8.1.1/32"),
		Peer: netip.MustParsePrefix("10.9.0.0/16")})
	l := b.k.LAN()
	if want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}; !reflect.DeepEqual(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
	if want := []netip.Addr{netip.MustParseAddr("10.0.0.20")}; !reflect.DeepEqual(l.Addresses, want) {
		t.Errorf("addresses = %v, want %v", l.Addresses, want)
	}
	for a, want := range map[string]bool{"10.0.0.42": true, "10.2.0.2": false, "10.8.1.1": false, "10.9.3.4": false, "10.2.0.3": true} {
		if got := lan.FromHere(netip.MustParseAddr(a), l); got != want {
			t.Errorf("FromHere(%s) = %v, want %v", a, got, want)
		}
	}
}

// The uplinks are where apps reach the internet: the LAN's interfaces first, then a tunnel with a default route in any
// table; not loopback, not one whose default route is unreachable or a blackhole, and not another link with a default
// route in another table (a local virtual network's bridge).
func TestTheUplinksAreTheLANAndTheTunnels(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().link("lo", "", lo).link("wg0", "wireguard", ptp).link("eth0", "", 0).link("eth1", "", 0).link("docker0", "bridge", 0).
		link("virbr0", "bridge", 0).link("eth2", "", 0).
		route(100, "0.0.0.0/0", gw, "virbr0").route(101, "::/0", gw, "eth2").
		route(51820, "0.0.0.0/0", onLink, "wg0").route(51820, "::/0", onLink, "wg0").
		route(mainTable, "0.0.0.0/0", gw, "eth0").
		routeType(mainTable, "::/0", 7, "eth1").
		route(mainTable, "172.17.0.0/16", onLink, "docker0").
		route(mainTable, "::/0", onLink, "lo").k
	if got, want := k.Uplinks(), []string{"eth0", "wg0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uplinks = %v, want %v", got, want)
	}
}

// OpenVPN's def1 (redirect-gateway def1) puts 0.0.0.0/1 and 128.0.0.0/1 through its tunnel instead of a default
// route (#148). Routes of one tunnel, in one table, that cover the internet count as its default route: two halves,
// quarters, IPv6's ::/1 and 8000::/1, OpenVPN's IPv6 redirect (all of 2000::/3), a WireGuard client's everything but
// the private ranges, or a BGP feed of every /16. One half, halves on two interfaces or in two tables, a blackhole half,
// a public range left out, or a link that isn't a tunnel don't. The LAN keeps to real default routes.
func TestRoutesThatCoverTheInternetAreATunnelsDefaultRoute(t *testing.T) {
	testenv.Isolate(t)
	b := kernel().link("eth0", "", 0).link("eth1", "", 0)
	for _, n := range []string{"def1", "half", "split-a", "split-b", "tables", "quarters", "v6", "blackhole", "ovpn6",
		"allowed", "gap", "private", "bgp"} {
		b.link(n, "tun", ptp)
	}
	b.route(mainTable, "0.0.0.0/0", gw, "eth0").
		route(mainTable, "0.0.0.0/1", onLink, "def1").route(mainTable, "128.0.0.0/1", onLink, "def1").
		route(mainTable, "0.0.0.0/1", onLink, "half").route(mainTable, "10.8.0.0/24", onLink, "half").
		route(mainTable, "0.0.0.0/1", onLink, "split-a").route(mainTable, "128.0.0.0/1", onLink, "split-b").
		route(mainTable, "0.0.0.0/1", onLink, "tables").route(100, "128.0.0.0/1", onLink, "tables").
		route(100, "0.0.0.0/2", gw, "quarters").route(100, "64.0.0.0/2", gw, "quarters").
		route(100, "128.0.0.0/1", gw, "quarters").
		route(mainTable, "::/1", onLink, "v6").route(mainTable, "8000::/1", onLink, "v6").
		route(mainTable, "0.0.0.0/1", onLink, "blackhole").routeType(mainTable, "128.0.0.0/1", 6, "blackhole").
		route(mainTable, "::/3", onLink, "ovpn6").route(mainTable, "2000::/4", onLink, "ovpn6").
		route(mainTable, "3000::/4", onLink, "ovpn6").route(mainTable, "fc00::/7", onLink, "ovpn6").
		route(mainTable, "10.0.0.0/8", onLink, "private").route(mainTable, "172.16.0.0/12", onLink, "private").
		route(mainTable, "192.168.0.0/16", onLink, "private").route(mainTable, "fd00::/8", onLink, "private").
		route(mainTable, "0.0.0.0/1", gw, "eth1").route(mainTable, "128.0.0.0/1", gw, "eth1")
	// 0.0.0.0/0 but 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16, as WireGuard's AllowedIPs calculators write it.
	allowed := strings.Fields("0.0.0.0/5 8.0.0.0/7 11.0.0.0/8 12.0.0.0/6 16.0.0.0/4 32.0.0.0/3 64.0.0.0/2 128.0.0.0/3 " +
		"160.0.0.0/5 168.0.0.0/6 172.0.0.0/12 172.32.0.0/11 172.64.0.0/10 172.128.0.0/9 173.0.0.0/8 174.0.0.0/7 " +
		"176.0.0.0/4 192.0.0.0/9 192.128.0.0/11 192.160.0.0/13 192.169.0.0/16 192.170.0.0/15 192.172.0.0/14 " +
		"192.176.0.0/12 192.192.0.0/10 193.0.0.0/8 194.0.0.0/7 196.0.0.0/6 200.0.0.0/5 208.0.0.0/4 224.0.0.0/3")
	for _, p := range allowed {
		b.route(mainTable, p, onLink, "allowed")
		if p != "8.0.0.0/7" {
			b.route(mainTable, p, onLink, "gap")
		}
	}
	b.route(mainTable, "9.0.0.0/8", onLink, "gap") // 8.0.0.0/8 left out
	for i := 65535; i >= 0; i-- {                  // every /16, last first
		b.route(51820, netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(i >> 8), byte(i)}), 16).String(), gw, "bgp")
	}
	if got, want := b.k.Uplinks(), []string{"eth0", "def1", "quarters", "v6", "ovpn6", "allowed", "bgp"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uplinks = %v, want %v", got, want)
	}
	if got := b.k.Interfaces(); !reflect.DeepEqual(got, []string{"eth0"}) {
		t.Errorf("interfaces = %v, want eth0 only: covering routes make no LAN", got)
	}
}

// A Network is one reading of the kernel: the LAN, its prefixes and the uplinks together.
func TestTheNetworkIsOneReading(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().link("lo", "", lo).link("eth0", "", 0).link("docker0", "bridge", 0).link("wg0", "wireguard", ptp).
		addr("eth0", "192.168.1.20/24", 0).addr("eth0", "2001:db8:1::20/64", 0).
		route(mainTable, "0.0.0.0/0", gw, "eth0").route(51820, "0.0.0.0/0", onLink, "wg0").k
	n := k.Network()
	ifaces, prefixes := k.OnLink()
	if !reflect.DeepEqual(n, lan.Network{LAN: ifaces, Prefixes: prefixes, Uplinks: []string{"eth0", "wg0"}}) || len(prefixes) != 2 {
		t.Errorf("network = %+v, want the LAN %v %v and the uplinks eth0 and wg0", n, ifaces, prefixes)
	}
}

func TestOnlyThisMachineAndTheLANAreFromHere(t *testing.T) {
	testenv.Isolate(t)
	l := lan.LAN{Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}, Interfaces: []string{"enp5s0"}}
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "127.8.9.1": true, "::1": true, "::ffff:127.0.0.1": true,
		"192.168.1.42": true, "::ffff:192.168.1.42": true, "fe80::1%enp5s0": true,
		"fe80::1": false, "fe80::1%docker0": false, "fe80::1%virbr0": false, "fe80::1%podman0": false,
		"192.168.2.1": false, "172.17.0.2": false, "100.64.0.7": false, "203.0.113.9": false, "2001:db8::1": false,
		"169.254.1.1": false,
	} {
		if got := lan.FromHere(netip.MustParseAddr(addr), l); got != want {
			t.Errorf("FromHere(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestTheMachinesNamesAreItsHostnameItsAddressesAndLoopback(t *testing.T) {
	testenv.Isolate(t)
	l := lan.LAN{Addresses: []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fd00:1::20")}}
	got := lan.Names("Study-PC.home.arpa", l)
	want := []string{"127.0.0.1", "::1", "localhost", "192.168.1.20", "fd00:1::20", "study-pc.home.arpa", "study-pc", "study-pc.local"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

// On an IPv6-only home network the LAN's ULA is this machine's address on it, and the link must carry it (#68).
func TestTheLANAddressIsIPv6WhenTheLANHasNoIPv4(t *testing.T) {
	testenv.Isolate(t)
	l := lan.LAN{Prefixes: []netip.Prefix{netip.MustParsePrefix("fd00:1::/64")}, Interfaces: []string{"eth0"},
		Addresses: []netip.Addr{netip.MustParseAddr("fd00:1::20")}}
	if got := l.Address(); got != netip.MustParseAddr("fd00:1::20") {
		t.Errorf("Address() = %v, want fd00:1::20 (an IPv6-only LAN)", got)
	}
	l.Addresses = append(l.Addresses, netip.MustParseAddr("192.168.1.20"))
	if got := l.Address(); got != netip.MustParseAddr("192.168.1.20") {
		t.Errorf("Address() = %v, want the IPv4 address when there is one", got)
	}
	if got := (lan.LAN{}).Address(); got.IsValid() {
		t.Errorf("Address() with no address on the LAN = %v, want none", got)
	}
}

const (
	mainTable = 254
	temporary = 0x01
	tentative = 0x40
	permanent = 0x80
	gw        = true
	onLink    = false
)

type linkFlags int

const (
	ptp linkFlags = 1 << iota
	lo
)

// builder makes a Kernel: links by name, then their addresses and routes.
type builder struct{ k lan.Kernel }

func kernel() *builder { return &builder{} }

func (b *builder) link(name, kind string, f linkFlags) *builder {
	b.k.Links = append(b.k.Links, lan.Link{Index: len(b.k.Links) + 1, Name: name, Kind: kind,
		PointToPoint: f&ptp != 0, Loopback: f&lo != 0})
	return b
}

func (b *builder) index(name string) int {
	for _, l := range b.k.Links {
		if l.Name == name {
			return l.Index
		}
	}
	panic("no link " + name)
}

func (b *builder) addr(name, prefix string, flags uint32) *builder {
	b.k.Addrs = append(b.k.Addrs, lan.Addr{Index: b.index(name), Prefix: netip.MustParsePrefix(prefix), Flags: flags})
	return b
}

func (b *builder) route(table uint32, dst string, gateway bool, name string) *builder {
	b.k.Routes = append(b.k.Routes, lan.Route{Table: table, Type: 1, Dst: netip.MustParsePrefix(dst), Gateway: gateway,
		Index: b.index(name)})
	return b
}

func (b *builder) routeType(table uint32, dst string, typ uint8, name string) *builder {
	b.k.Routes = append(b.k.Routes, lan.Route{Table: table, Type: typ, Dst: netip.MustParsePrefix(dst), Index: b.index(name)})
	return b
}

// A home network whose ISP hands out a global /64 and no IPv4 (#73): the /64 on the default route's interface is the
// LAN, but no wider prefix, nothing behind a router, and no other interface's.
func ipv6OnlyMachine() *builder {
	return kernel().link("lo", "", lo).link("eth0", "", 0).link("docker0", "bridge", 0).
		route(mainTable, "::/0", gw, "eth0").                // the default route
		route(mainTable, "2001:db8:1::/64", onLink, "eth0"). // on the link: the /64 the router advertises
		route(mainTable, "fd00:1::/64", onLink, "eth0").
		route(mainTable, "2001:db8::/48", onLink, "eth0").      // on the link but wider than the machine's own
		route(mainTable, "2001:db8:2::/64", gw, "eth0").        // behind the router
		route(mainTable, "2001:db8:3::/64", onLink, "docker0"). // a bridge's
		route(mainTable, "2001:db8:7::/48", onLink, "eth0").    // a /48 on the link: too wide to be a home's
		route(mainTable, "2001:db8:4::/64", onLink, "eth0").
		routeType(255, "2001:db8:9::5/128", 2, "eth0"). // the local table's own /128
		routeType(255, "2001:db8:7::5/128", 2, "eth0").
		route(51820, "2001:db8:6::/64", onLink, "eth0"). // on the link, but in another table
		route(mainTable, "::/0", onLink, "eth0").        // a default route without a router
		route(mainTable, "fe80::/64", onLink, "eth0").
		addr("eth0", "2001:db8:1::abcd/64", temporary). // a privacy address
		addr("eth0", "2001:db8:1::20/64", permanent).
		addr("eth0", "fd00:1::20/64", permanent).
		addr("eth0", "2001:db8:1::99/64", tentative). // not yet usable
		addr("eth0", "2001:db8:2::7/64", permanent).  // its /64 is reached through the router
		addr("eth0", "2001:db8:9::5/128", permanent). // DHCPv6, with no prefix on the link
		addr("eth0", "2001:db8:4::5/128", permanent). // DHCPv6, on a /64 the router says is on the link
		addr("eth0", "2001:db8:6::5/128", permanent). // DHCPv6, on a /64 only another table has
		addr("eth0", "2001:db8:7::5/48", permanent).
		addr("eth0", "2001:db8:8::5/0", permanent).
		addr("eth0", "fe80::20/64", permanent).
		addr("docker0", "2001:db8:3::1/64", permanent).
		addr("lo", "::1/128", permanent)
}

func TestTheLANIncludesTheOnLinkGlobalIPv6Prefix(t *testing.T) {
	testenv.Isolate(t)
	l := ipv6OnlyMachine().k.LAN()
	if want := []string{"eth0"}; !reflect.DeepEqual(l.Interfaces, want) {
		t.Errorf("interfaces = %v, want %v", l.Interfaces, want)
	}
	wantPrefixes := []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("2001:db8:4::/64"),
		netip.MustParsePrefix("fd00:1::/64")}
	if !sameSet(l.Prefixes, wantPrefixes) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, wantPrefixes)
	}
	want := []netip.Addr{netip.MustParseAddr("fd00:1::20"), netip.MustParseAddr("2001:db8:1::20"),
		netip.MustParseAddr("2001:db8:4::5"), netip.MustParseAddr("2001:db8:1::abcd")}
	if !reflect.DeepEqual(l.Addresses, want) {
		t.Errorf("addresses = %v, want %v: the ULA, then the stable global addresses (SLAAC's, then DHCPv6's), then the privacy one", l.Addresses, want)
	}
	if got := l.Address(); got != netip.MustParseAddr("fd00:1::20") {
		t.Errorf("Address() = %v, want the ULA", got)
	}
	for addr, wantHere := range map[string]bool{
		"2001:db8:1::42": true, "fd00:1::42": true, "fe80::42%eth0": true, "2001:db8:4::42": true,
		"2001:db8:2::9": false, "2001:db8:9::1": false, "2001:db8:7::9": false, "2001:db8:8::9": false, "2a00::1": false, "2001:db8:5::1": false, "2001:db8:3::2": false, "2001:db9::1": false,
	} {
		if got := lan.FromHere(netip.MustParseAddr(addr), l); got != wantHere {
			t.Errorf("FromHere(%s) = %v, want %v", addr, got, wantHere)
		}
	}
	names := lan.Names("study-pc", l)
	if !slices.Contains(names, "2001:db8:1::20") || slices.Contains(names, "2001:db8:2::7") {
		t.Errorf("names = %v, want the machine's global address on the LAN, and not one behind a router", names)
	}
}

func TestAGlobalOnlyLANsAddressIsItsStableGlobalAddress(t *testing.T) {
	testenv.Isolate(t)
	b := ipv6OnlyMachine()
	b.k.Addrs = nil
	b.addr("eth0", "2001:db8:1::abcd/64", temporary).addr("eth0", "2001:db8:1::20/64", permanent)
	if got := b.k.LAN().Address(); got != netip.MustParseAddr("2001:db8:1::20") {
		t.Errorf("Address() = %v, want the stable global address, not the privacy one", got)
	}
}

func TestTheLANsIPv4AddressComesFirst(t *testing.T) {
	testenv.Isolate(t)
	b := ipv6OnlyMachine().route(mainTable, "0.0.0.0/0", gw, "eth0").
		addr("eth0", "100.64.0.9/10", 0).addr("eth0", "192.168.1.20/24", 0).addr("docker0", "172.17.0.1/16", 0)
	l := b.k.LAN()
	if got := l.Address(); got != netip.MustParseAddr("192.168.1.20") {
		t.Errorf("Address() = %v, want 192.168.1.20", got)
	}
	if !lan.FromHere(netip.MustParseAddr("192.168.1.42"), l) || lan.FromHere(netip.MustParseAddr("100.64.0.7"), l) {
		t.Errorf("prefixes = %v, want the private IPv4 network and not the shared one", l.Prefixes)
	}
}

// A LAN without a default route has no address a link could carry.
func TestALANWithoutADefaultRouteHasNoAddress(t *testing.T) {
	testenv.Isolate(t)
	b := ipv6OnlyMachine()
	b.k.Routes = nil
	b.route(mainTable, "fd00:1::/64", onLink, "eth0")
	if l := b.k.LAN(); l.Address().IsValid() || len(l.Prefixes) != 0 {
		t.Errorf("with no default route: %+v, want no LAN", l)
	}
}

func sameSet[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// A listener bound to 0.0.0.0 answers IPv4 only: the link carries the LAN's IPv4 address or none. One bound to ::
// answers both.
func TestTheLinksAddressFollowsTheListenersFamily(t *testing.T) {
	testenv.Isolate(t)
	v6only := lan.LAN{Addresses: []netip.Addr{netip.MustParseAddr("fd00:1::20")}}
	both := lan.LAN{Addresses: []netip.Addr{netip.MustParseAddr("fd00:1::20"), netip.MustParseAddr("192.168.1.20")}}
	for _, c := range []struct {
		l        lan.LAN
		listener string
		want     string
	}{
		{v6only, "0.0.0.0", ""},
		{v6only, "::", "fd00:1::20"},
		{both, "0.0.0.0", "192.168.1.20"},
		{both, "::", "192.168.1.20"},
		{lan.LAN{}, "::", ""},
	} {
		got := c.l.AddressFor(netip.MustParseAddr(c.listener))
		if want := c.want; (want == "" && got.IsValid()) || (want != "" && got != netip.MustParseAddr(want)) {
			t.Errorf("AddressFor(%s) on %v = %v, want %q", c.listener, c.l.Addresses, got, want)
		}
	}
}
