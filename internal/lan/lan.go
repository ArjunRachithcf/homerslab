// Package lan says what this machine's home network is: the private networks on the interfaces that carry its
// default route in the main routing table, and the global IPv6 prefixes on those links, as the kernel reports them
// over netlink. Containers' bridges carry no default route, and a VPN's or the mesh's tunnel is never the LAN, nor is
// any range of theirs (#108).
package lan

import (
	"net/netip"
	"slices"
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

// Kernel is what the kernel says of this machine's network: its links, their addresses, and the routes of every
// table (ReadKernel).
type Kernel struct {
	Links  []Link
	Addrs  []Addr
	Routes []Route
}

// Link is a network interface.
type Link struct {
	Index int
	Name  string
	// Kind is the link's type as the kernel names it: "wireguard", "tun" (tun and tap), "veth", "bridge", "dummy",
	// or "" for a physical one.
	Kind                   string
	PointToPoint, Loopback bool
}

// Addr is one of the machine's addresses, with its prefix length, on the interface whose index it carries.
type Addr struct {
	Index  int
	Prefix netip.Prefix
	// Flags are the address's IFA_F_ flags: temporary, tentative, deprecated, failed duplicate address detection.
	Flags uint32
}

// Route is a route of one table: a multipath route is one Route per next hop.
type Route struct {
	Table uint32 // the main table is 254
	Type  uint8  // a unicast route is 1; local, broadcast, unreachable and blackhole ones are others
	Dst   netip.Prefix
	// Gateway: the route goes through a router, not straight onto the link.
	Gateway bool
	Index   int // the interface it goes out of
}

// The kernel's numbers this package reads (linux/rtnetlink.h, linux/if_addr.h).
const (
	mainTable     = 254
	unicast       = 1
	ifaTemporary  = 0x01
	ifaDADFailed  = 0x08
	ifaDeprecated = 0x20
	ifaTentative  = 0x40
)

// tunnelKinds are the kinds of link that carry a VPN or a mesh, not a home network.
var tunnelKinds = []string{"wireguard", "tun", "ipip", "ip6tnl", "sit", "gre", "gretap", "ip6gre", "ip6gretap",
	"vti", "vti6", "xfrm", "ppp", "vxlan", "geneve", "openvpn"}

// meshRanges are the ranges the meshes HomersLab runs give out (#14 Q5, ADR 0005): CGNAT (NetBird, Headscale and
// Tailscale) and Tailscale's ULA, whichever mesh this machine is on.
var meshRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

// Current is the LAN, read from the live machine; no LAN at all when the kernel can't be asked.
func Current() LAN {
	k, err := ReadKernel()
	if err != nil {
		return LAN{}
	}
	return k.LAN()
}

// OnLink is what the firewall table keeps apps away from, read from the live machine (Kernel.OnLink); nothing when
// the kernel can't be asked.
func OnLink() (interfaces []string, prefixes []netip.Prefix) {
	k, err := ReadKernel()
	if err != nil {
		return nil, nil
	}
	return k.OnLink()
}

// Interfaces are the LAN's interfaces: those that carry a default route, IPv4 or IPv6, in the main table (#108). Not
// loopback, and not a VPN's or the mesh's (a tunnel: point-to-point, or a tunnel's kind of link), even when a
// full-tunnel VPN puts its default route in the main table; and never through another table, where wg-quick puts a
// full tunnel's (51820).
func (k Kernel) Interfaces() []string {
	var out []string
	for _, r := range k.Routes {
		if r.Table != mainTable || r.Type != unicast || r.Dst.Bits() != 0 {
			continue
		}
		l, ok := k.link(r.Index)
		if ok && !l.Loopback && !l.tunnel() && !slices.Contains(out, l.Name) {
			out = append(out, l.Name)
		}
	}
	return out
}

// LAN is the LAN this kernel describes.
//
// The LAN is, on its interfaces (Interfaces):
//   - the private IPv4 networks (RFC 1918) and IPv6 ULAs (#14, Q5);
//   - the global IPv6 prefixes on the link itself, so that a home whose ISP hands out only those can set up (#73):
//     a /64 or narrower that the main table says is on the link (no router in between) and that holds one of the
//     machine's own addresses there, from SLAAC (a /64) or DHCPv6 (a /128). Never a wider prefix, the default route's,
//     or one reached through a router.
//
// Never a prefix that overlaps the mesh's ranges (meshRanges, and every address on a tunnel): a rogue router
// advertising the mesh's ULA /64 on the LAN doesn't make mesh peers the LAN (#108).
//
// Its addresses are the machine's own on those networks, the best first: stable before temporary (privacy) ones, and
// none that is still tentative or failed duplicate address detection.
func (k Kernel) LAN() LAN {
	l := LAN{Interfaces: k.Interfaces()}
	onLink := k.onLinkPrefixes()
	mesh := k.meshPrefixes()
	var later []netip.Addr // temporary addresses, after the stable ones
	addPrefix := func(p netip.Prefix) {
		if !slices.Contains(l.Prefixes, p) {
			l.Prefixes = append(l.Prefixes, p)
		}
	}
	for _, name := range l.Interfaces {
		var v4, ula, global []ipv6Addr
		for _, a := range k.addrsOf(name) {
			switch {
			case a.Prefix.Addr().Is4() && a.Prefix.Addr().IsPrivate():
				v4 = append(v4, ipv6Addr{Addr: a, lan: []netip.Prefix{a.Prefix.Masked()}})
			case a.Prefix.Addr().Is4():
			case a.Prefix.Addr().IsPrivate():
				ula = append(ula, ipv6Addr{Addr: a, lan: []netip.Prefix{a.Prefix.Masked()}})
			case a.Prefix.Addr().IsGlobalUnicast():
				var in []netip.Prefix
				for _, p := range onLink[name] {
					if p.Contains(a.Prefix.Addr()) {
						in = append(in, p)
					}
				}
				global = append(global, ipv6Addr{Addr: a, lan: in})
			}
		}
		for _, a := range slices.Concat(v4, ula, global) {
			a.lan = slices.DeleteFunc(a.lan, func(p netip.Prefix) bool { return overlapsAny(p, mesh) })
			if len(a.lan) == 0 {
				continue
			}
			for _, p := range a.lan {
				addPrefix(p)
			}
			switch {
			case a.Flags&(ifaTentative|ifaDADFailed) != 0:
			case a.Flags&(ifaTemporary|ifaDeprecated) != 0 && a.Prefix.Addr().Is6():
				later = append(later, a.Prefix.Addr())
			default:
				l.Addresses = append(l.Addresses, a.Prefix.Addr())
			}
		}
	}
	l.Addresses = append(l.Addresses, later...)
	return l
}

// OnLink is what the firewall table keeps apps away from: the LAN's interfaces, and every prefix on them, private or
// global (a LAN may use global IPv6 addresses), but link-local, which the table covers whole, and any that overlaps
// the mesh's ranges.
func (k Kernel) OnLink() (interfaces []string, prefixes []netip.Prefix) {
	interfaces = k.Interfaces()
	mesh := k.meshPrefixes()
	for _, name := range interfaces {
		for _, a := range k.addrsOf(name) {
			p := a.Prefix.Masked()
			if !p.Addr().IsLinkLocalUnicast() && !p.Addr().IsLoopback() && !overlapsAny(p, mesh) && !slices.Contains(prefixes, p) {
				prefixes = append(prefixes, p)
			}
		}
	}
	return interfaces, prefixes
}

func (l Link) tunnel() bool { return l.PointToPoint || slices.Contains(tunnelKinds, l.Kind) }

func (k Kernel) link(index int) (Link, bool) {
	for _, l := range k.Links {
		if l.Index == index {
			return l, true
		}
	}
	return Link{}, false
}

func (k Kernel) addrsOf(name string) []Addr {
	var out []Addr
	for _, a := range k.Addrs {
		if l, ok := k.link(a.Index); ok && l.Name == name {
			out = append(out, a)
		}
	}
	return out
}

// meshPrefixes are the ranges no LAN prefix may overlap: the meshes' own, and every network on a tunnel (the mesh's
// interface, a VPN's), but link-local, which every interface has.
func (k Kernel) meshPrefixes() []netip.Prefix {
	out := slices.Clone(meshRanges)
	for _, a := range k.Addrs {
		if l, ok := k.link(a.Index); ok && l.tunnel() && !a.Prefix.Addr().IsLinkLocalUnicast() {
			out = append(out, a.Prefix.Masked())
		}
	}
	return out
}

func overlapsAny(p netip.Prefix, list []netip.Prefix) bool {
	return slices.ContainsFunc(list, p.Overlaps)
}

// onLinkPrefixes are the IPv6 prefixes on the link, per interface, from the main table: unicast routes from /64 to
// /127 with no router in between. Not the default route (::/0) or any route wider than a /64, not the machine's own
// /128s, which the local table lists, and nothing another table holds.
func (k Kernel) onLinkPrefixes() map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	for _, r := range k.Routes {
		if r.Table != mainTable || r.Type != unicast || r.Gateway || !r.Dst.Addr().Is6() || r.Dst.Bits() < 64 || r.Dst.Bits() > 127 {
			continue
		}
		if l, ok := k.link(r.Index); ok {
			out[l.Name] = append(out[l.Name], r.Dst.Masked())
		}
	}
	return out
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

// ipv6Addr is an address with the LAN's prefixes that hold it.
type ipv6Addr struct {
	Addr
	lan []netip.Prefix
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
