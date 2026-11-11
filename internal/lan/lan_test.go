package lan_test

import (
	"net/netip"
	"reflect"
	"slices"
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

// A prefix that overlaps the mesh's ranges is never the LAN, nor kept from the firewall's list as one (#108): a rogue
// router advertisement of the mesh's ULA /64 on the LAN, a LAN using the mesh's CGNAT range, or Tailscale's ULA.
func TestThePrefixesThatOverlapTheMeshAreRefused(t *testing.T) {
	testenv.Isolate(t)
	k := kernel().link("eth0", "", 0).link("wt0", "wireguard", ptp).
		addr("wt0", "100.70.1.2/16", 0).addr("wt0", "fd10:abcd:0:1::2/64", 0).addr("wt0", "fe80::2/64", 0).
		addr("eth0", "192.168.1.20/24", 0).
		addr("eth0", "fd00:1::20/64", 0).
		addr("eth0", "fd10:abcd:0:1::99/64", 0). // from the rogue advertisement
		addr("eth0", "fd10:abcd::99/48", 0).     // a wider one holding the mesh's
		addr("eth0", "fd7a:115c:a1e0:ab12::5/64", 0).
		addr("eth0", "2001:db8:1::20/64", 0).
		addr("eth0", "100.70.9.9/24", 0).
		addr("eth0", "fe80::20/64", 0).
		route(mainTable, "0.0.0.0/0", gw, "eth0").route(mainTable, "::/0", gw, "eth0").
		route(mainTable, "fd10:abcd:0:1::/64", onLink, "eth0").route(mainTable, "2001:db8:1::/64", onLink, "eth0").
		route(mainTable, "fd10:abcd:0:1::/64", onLink, "wt0").k
	want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fd00:1::/64"),
		netip.MustParsePrefix("2001:db8:1::/64")}
	l := k.LAN()
	if !sameSet(l.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", l.Prefixes, want)
	}
	if wantAddrs := []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fd00:1::20"),
		netip.MustParseAddr("2001:db8:1::20")}; !reflect.DeepEqual(l.Addresses, wantAddrs) {
		t.Errorf("addresses = %v, want %v", l.Addresses, wantAddrs)
	}
	for _, a := range []string{"fd10:abcd:0:1::3", "fd10:abcd::1", "fd7a:115c:a1e0:ab12::9", "100.70.9.1"} {
		if lan.FromHere(netip.MustParseAddr(a), l) {
			t.Errorf("FromHere(%s): a mesh address is the LAN", a)
		}
	}
	if _, prefixes := k.OnLink(); !sameSet(prefixes, want) {
		t.Errorf("OnLink prefixes = %v, want %v", prefixes, want)
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
