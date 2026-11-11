// Package lan says what this machine's home network is: the private networks on the interfaces that carry its
// default route, and the global IPv6 prefixes on those links. Containers' bridges and the mesh carry no default
// route, so they are not the LAN.
package lan

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LAN is this machine's home network: the interfaces that carry the default route, their private prefixes, and
// this machine's own private addresses on them.
type LAN struct {
	Prefixes   []netip.Prefix
	Interfaces []string
	Addresses  []netip.Addr
}

// FromHere says whether a request from addr comes from this machine or its LAN. A link-local address is the same on
// every interface, so it counts only with the zone of one of the LAN's interfaces: not a container's bridge, a VM's
// or the mesh's.
func FromHere(addr netip.Addr, l LAN) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() {
		return true
	}
	if addr.IsLinkLocalUnicast() {
		return addr.Is6() && addr.Zone() != "" && slices.Contains(l.Interfaces, addr.Zone())
	}
	addr = addr.WithZone("")
	for _, p := range l.Prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Current is the LAN, read from the live machine.
func Current() LAN { return Read(os.DirFS("/proc"), ipv4Addrs) }

// Read is the LAN of the machine whose /proc is proc, with v4 listing an interface's IPv4 addresses (nil: none).
//
// The LAN is, on the interfaces that carry the default route:
//   - the private IPv4 networks (RFC 1918) and IPv6 ULAs (#14, Q5);
//   - the global IPv6 prefixes on the link itself, so that a home whose ISP hands out only those can set up (#73):
//     a /64 or narrower that the routing table says is on the link (no router in between) and that holds one of the
//     machine's own addresses there, from SLAAC (a /64) or DHCPv6 (a /128). Never a wider prefix, the default route's,
//     or one reached through a router.
//
// Its addresses are the machine's own on those networks, the best first: stable before temporary (privacy) ones, and
// none that is still tentative or failed duplicate address detection.
func Read(proc fs.FS, v4 func(iface string) []netip.Prefix) LAN {
	l := LAN{Interfaces: DefaultRouteInterfaces(proc)}
	onLink := onLinkPrefixes(proc)
	v6 := ipv6Addrs(proc)
	var later []netip.Addr // temporary addresses, after the stable ones
	addPrefix := func(p netip.Prefix) {
		if !slices.Contains(l.Prefixes, p) {
			l.Prefixes = append(l.Prefixes, p)
		}
	}
	for _, name := range l.Interfaces {
		if v4 != nil {
			for _, p := range v4(name) {
				if p.Addr().Is4() && p.Addr().IsPrivate() {
					addPrefix(p.Masked())
					l.Addresses = append(l.Addresses, p.Addr())
				}
			}
		}
		var ula, global []ipv6Addr
		for _, a := range v6[name] {
			switch {
			case a.prefix.Addr().IsPrivate():
				a.lan = []netip.Prefix{a.prefix.Masked()}
				ula = append(ula, a)
			case a.prefix.Addr().IsGlobalUnicast():
				for _, p := range onLink[name] {
					if p.Contains(a.prefix.Addr()) {
						a.lan = append(a.lan, p)
					}
				}
				if a.lan != nil {
					global = append(global, a)
				}
			}
		}
		for _, a := range append(ula, global...) {
			for _, p := range a.lan {
				addPrefix(p)
			}
			switch {
			case a.flags&(ifaTentative|ifaDADFailed) != 0:
			case a.flags&(ifaTemporary|ifaDeprecated) != 0:
				later = append(later, a.prefix.Addr())
			default:
				l.Addresses = append(l.Addresses, a.prefix.Addr())
			}
		}
	}
	l.Addresses = append(l.Addresses, later...)
	return l
}

// OnLink is what the firewall table keeps apps away from: the interfaces that carry the default route, and every
// prefix on them, private or global (a LAN may use global IPv6 addresses), but link-local, which the table covers
// whole.
func OnLink() (interfaces []string, prefixes []netip.Prefix) {
	interfaces = DefaultRouteInterfaces(os.DirFS("/proc"))
	for _, name := range interfaces {
		for _, p := range addrs(name) {
			if !p.Addr().IsLinkLocalUnicast() && !p.Addr().IsLoopback() {
				prefixes = append(prefixes, p.Masked())
			}
		}
	}
	return interfaces, prefixes
}

// Address is this machine's address on the LAN, for the links it prints: its IPv4 address if it has one, else its
// best IPv6 address on the LAN (a ULA before a global one); or the zero Addr if it has none.
func (l LAN) Address() netip.Addr {
	for _, a := range l.Addresses {
		if a.Is4() {
			return a
		}
	}
	for _, a := range l.Addresses {
		if a.Is6() && !a.IsLinkLocalUnicast() { // Read lists none, but a LAN made by hand might: a link can't carry its zone
			return a
		}
	}
	return netip.Addr{}
}

// AddressFor is Address for a listener bound to an unspecified address: 0.0.0.0 answers IPv4 only, so its links
// carry the LAN's IPv4 address or none; :: answers both.
func (l LAN) AddressFor(listener netip.Addr) netip.Addr {
	if a := l.Address(); listener.Is6() || a.Is4() {
		return a
	}
	return netip.Addr{}
}

// Names are the names a browser on this machine or its LAN may use for it: loopback, its LAN addresses, and its
// hostname, short and with .local (mDNS). Lower case.
func Names(hostname string, l LAN) []string {
	names := []string{"127.0.0.1", "::1", "localhost"}
	for _, a := range l.Addresses {
		names = append(names, a.String())
	}
	if h := strings.ToLower(strings.TrimSuffix(hostname, ".")); h != "" {
		short, _, _ := strings.Cut(h, ".")
		names = append(names, h)
		if short != h {
			names = append(names, short)
		}
		names = append(names, short+".local")
	}
	return names
}

// ipv4Addrs lists an interface's IPv4 addresses with their prefixes.
func ipv4Addrs(name string) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range addrs(name) {
		if p.Addr().Is4() {
			out = append(out, p)
		}
	}
	return out
}

// addrs lists an interface's addresses with their prefixes.
func addrs(name string) []netip.Prefix {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	list, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, a := range list {
		if n, ok := a.(*net.IPNet); ok {
			if p, err := netip.ParsePrefix(n.String()); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// The flags of an IPv6 address in /proc/net/if_inet6 (linux/if_addr.h).
const (
	ifaTemporary  = 0x01
	ifaDADFailed  = 0x08
	ifaDeprecated = 0x20
	ifaTentative  = 0x40
)

type ipv6Addr struct {
	prefix netip.Prefix // the address, with its prefix length
	flags  uint64
	lan    []netip.Prefix // the LAN's prefixes that hold it
}

// ipv6Addrs lists the IPv6 addresses per interface from /proc/net/if_inet6: the address, the interface's index, the
// prefix length, the scope and the flags in hex, then the interface's name.
func ipv6Addrs(proc fs.FS) map[string][]ipv6Addr {
	out := map[string][]ipv6Addr{}
	for _, f := range lines(proc, "net/if_inet6") {
		if len(f) != 6 {
			continue
		}
		a, ok := hexAddr(f[0])
		bits, err1 := strconv.ParseUint(f[2], 16, 8)
		flags, err2 := strconv.ParseUint(f[4], 16, 32)
		if !ok || err1 != nil || err2 != nil || bits > 128 {
			continue
		}
		out[f[5]] = append(out[f[5]], ipv6Addr{prefix: netip.PrefixFrom(a, int(bits)), flags: flags})
	}
	return out
}

// onLinkPrefixes are the IPv6 prefixes on the link, per interface, from /proc/net/ipv6_route (the destination and its
// prefix length, the source and its, the next hop, metric, use and reference counts, the flags, and the interface):
// routes from /64 to /127 with no router in between. Not the default route (::/0) or any route wider than a /64, and
// not the machine's own /128s, which the local table lists.
func onLinkPrefixes(proc fs.FS) map[string][]netip.Prefix {
	const rtfUp, rtfGateway, rtfReject, rtfLocal = 0x0001, 0x0002, 0x0200, 0x80000000
	out := map[string][]netip.Prefix{}
	for _, f := range lines(proc, "net/ipv6_route") {
		if len(f) != 10 {
			continue
		}
		dest, ok1 := hexAddr(f[0])
		via, ok2 := hexAddr(f[4])
		bits, err1 := strconv.ParseUint(f[1], 16, 8)
		flags, err2 := strconv.ParseUint(f[8], 16, 32)
		if !ok1 || !ok2 || err1 != nil || err2 != nil || bits < 64 || bits > 127 ||
			flags&rtfUp == 0 || flags&(rtfGateway|rtfReject|rtfLocal) != 0 || !via.IsUnspecified() {
			continue
		}
		out[f[9]] = append(out[f[9]], netip.PrefixFrom(dest, int(bits)).Masked())
	}
	return out
}

// hexAddr is an IPv6 address as /proc writes it: 32 hex digits.
func hexAddr(s string) (netip.Addr, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom16([16]byte(b)), true
}

// DefaultRouteInterfaces lists the interfaces that carry a default route, IPv4 or IPv6, from a /proc.
func DefaultRouteInterfaces(proc fs.FS) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && name != "lo" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	// net/route: Iface Destination Gateway Flags ...; the default route's destination and mask are 00000000.
	for i, f := range lines(proc, "net/route") {
		if i > 0 && len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			add(f[0])
		}
	}
	// net/ipv6_route: destination, its prefix length, ..., the interface last; ::/0 is the default.
	for _, f := range lines(proc, "net/ipv6_route") {
		if len(f) == 10 && f[0] == strings.Repeat("0", 32) && f[1] == "00" {
			add(f[9])
		}
	}
	return out
}

func lines(proc fs.FS, name string) [][]string {
	b, err := fs.ReadFile(proc, name)
	if err != nil {
		return nil
	}
	var out [][]string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		out = append(out, strings.Fields(sc.Text()))
	}
	return out
}

// Cached remembers what read returns for ttl, so a page's many requests don't each read /proc and the interfaces.
func Cached[T any](read func() T, ttl time.Duration) func() T {
	var mu sync.Mutex
	var at time.Time
	var last T
	return func() T {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > ttl {
			last, at = read(), time.Now()
		}
		return last
	}
}
