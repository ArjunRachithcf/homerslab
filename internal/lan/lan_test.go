package lan_test

import (
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestTheLANIsWhereTheDefaultRouteGoes(t *testing.T) {
	testenv.Isolate(t)
	proc := fstest.MapFS{
		"net/route": {Data: []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
			"enp5s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
			"enp5s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
			"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
			"wt0\t00004064\t00000000\t0001\t0\t0\t0\t0000C0FF\t0\t0\t0\n")},
		"net/ipv6_route": {Data: []byte(
			"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000001 00000000 00000003 wlp4s0\n" +
				"fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 enp5s0\n" +
				"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n")},
	}
	if got, want := lan.DefaultRouteInterfaces(proc), []string{"enp5s0", "wlp4s0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("interfaces = %v, want %v", got, want)
	}
	if got := lan.DefaultRouteInterfaces(fstest.MapFS{}); got != nil {
		t.Errorf("with no routes: %v", got)
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

// ipv6Route is a line of /proc/net/ipv6_route: destination, its prefix length, the next hop, the flags, the interface.
func ipv6Route(dest string, plen int, via string, flags uint32, iface string) string {
	hex := func(s string) string {
		a := netip.MustParseAddr(s).As16()
		return fmt.Sprintf("%x", a[:])
	}
	return fmt.Sprintf("%s %02x %s 00 %s 00000100 00000001 00000000 %08x %8s\n",
		hex(dest), plen, strings.Repeat("0", 32), hex(via), flags, iface)
}

// ifInet6 is a line of /proc/net/if_inet6: the address, the interface's index, the prefix length, scope, flags, name.
func ifInet6(addr string, plen int, scope, flags int, iface string) string {
	a := netip.MustParseAddr(addr).As16()
	return fmt.Sprintf("%x 02 %02x %02x %02x %8s\n", a[:], plen, scope, flags, iface)
}

const (
	rtfUp      = 0x0001
	rtfGateway = 0x0002
	global     = 0x00
	linkScope  = 0x20
	temporary  = 0x01
	tentative  = 0x40
	permanent  = 0x80
)

// A home network whose ISP hands out a global /64 and no IPv4 (#73): the /64 on the default route's interface is the
// LAN, but no wider prefix, nothing behind a router, and no other interface's.
func ipv6OnlyMachine() fstest.MapFS {
	return fstest.MapFS{
		"net/route": {Data: []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n")},
		"net/ipv6_route": {Data: []byte(
			ipv6Route("::", 0, "fe80::1", rtfUp|rtfGateway, "eth0") + // the default route
				ipv6Route("2001:db8:1::", 64, "::", rtfUp, "eth0") + // on the link: the /64 the router advertises
				ipv6Route("fd00:1::", 64, "::", rtfUp, "eth0") +
				ipv6Route("2001:db8::", 48, "::", rtfUp, "eth0") + // on the link but wider than the machine's own
				ipv6Route("2001:db8:2::", 64, "fe80::1", rtfUp|rtfGateway, "eth0") + // behind the router
				ipv6Route("2001:db8:3::", 64, "::", rtfUp, "docker0") + // a bridge's
				ipv6Route("2001:db8:7::", 48, "::", rtfUp, "eth0") + // a /48 on the link: too wide to be a home's
				ipv6Route("2001:db8:4::", 64, "::", rtfUp, "eth0") +
				ipv6Route("2001:db8:9::5", 128, "::", 0x80200001, "eth0") + // the local table's own /128
				ipv6Route("2001:db8:7::5", 128, "::", 0x80200001, "eth0") +
				ipv6Route("::", 0, "::", rtfUp, "eth0") + // a default route without a router
				ipv6Route("fe80::", 64, "::", rtfUp, "eth0"))},
		"net/if_inet6": {Data: []byte(
			ifInet6("2001:db8:1::abcd", 64, global, temporary, "eth0") + // a privacy address
				ifInet6("2001:db8:1::20", 64, global, permanent, "eth0") +
				ifInet6("fd00:1::20", 64, global, permanent, "eth0") +
				ifInet6("2001:db8:1::99", 64, global, tentative, "eth0") + // not yet usable
				ifInet6("2001:db8:2::7", 64, global, permanent, "eth0") + // its /64 is reached through the router
				ifInet6("2001:db8:9::5", 128, global, permanent, "eth0") + // DHCPv6, with no prefix on the link
				ifInet6("2001:db8:4::5", 128, global, permanent, "eth0") + // DHCPv6, on a /64 the router says is on the link
				ifInet6("2001:db8:7::5", 48, global, permanent, "eth0") +
				ifInet6("2001:db8:8::5", 0, global, permanent, "eth0") +
				ifInet6("fe80::20", 64, linkScope, permanent, "eth0") +
				ifInet6("2001:db8:3::1", 64, global, permanent, "docker0") +
				ifInet6("::1", 128, 0x10, permanent, "lo"))},
	}
}

func TestTheLANIncludesTheOnLinkGlobalIPv6Prefix(t *testing.T) {
	testenv.Isolate(t)
	l := lan.Read(ipv6OnlyMachine(), nil)
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
	proc := ipv6OnlyMachine()
	proc["net/if_inet6"] = &fstest.MapFile{Data: []byte(
		ifInet6("2001:db8:1::abcd", 64, global, temporary, "eth0") + ifInet6("2001:db8:1::20", 64, global, permanent, "eth0"))}
	if got := lan.Read(proc, nil).Address(); got != netip.MustParseAddr("2001:db8:1::20") {
		t.Errorf("Address() = %v, want the stable global address, not the privacy one", got)
	}
}

func TestTheLANsIPv4AddressComesFirst(t *testing.T) {
	testenv.Isolate(t)
	proc := ipv6OnlyMachine()
	proc["net/route"] = &fstest.MapFile{Data: []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n")}
	v4 := func(iface string) []netip.Prefix {
		if iface != "eth0" {
			return nil
		}
		return []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24"), netip.MustParsePrefix("100.64.0.9/10")}
	}
	l := lan.Read(proc, v4)
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
	proc := ipv6OnlyMachine()
	proc["net/ipv6_route"] = &fstest.MapFile{Data: []byte(ipv6Route("fd00:1::", 64, "::", rtfUp, "eth0"))}
	if l := lan.Read(proc, nil); l.Address().IsValid() || len(l.Prefixes) != 0 {
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
