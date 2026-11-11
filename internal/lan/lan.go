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
	// Excluded are the mesh's and the tunnels' ranges inside the LAN's prefixes: never the LAN (#108).
	Excluded []netip.Prefix
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
	if slices.ContainsFunc(l.Excluded, func(p netip.Prefix) bool { return p.Contains(addr) }) {
		return false
	}
	return slices.ContainsFunc(l.Prefixes, func(p netip.Prefix) bool { return p.Contains(addr) })
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
	// Peer is the network at the other end of a point-to-point address (`ip addr add A peer P/len`), if it has one:
	// Prefix is then the machine's own end alone.
	Peer netip.Prefix
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
	"vti", "vti6", "xfrm", "ppp", "vxlan", "geneve", "ovpn"}

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

// Uplinks are the interfaces through which this machine reaches the internet, read from the live machine
// (Kernel.Uplinks); none when the kernel can't be asked.
func Uplinks() []string {
	k, err := ReadKernel()
	if err != nil {
		return nil
	}
	return k.Uplinks()
}

// Interfaces are the LAN's interfaces: those that carry a default route, IPv4 or IPv6, in the main table (#108). Not
// loopback, and not a VPN's or the mesh's (a tunnel: point-to-point, or a tunnel's kind of link), even when a
// full-tunnel VPN puts its default route in the main table; and never through another table, where wg-quick puts a
// full tunnel's (51820).
func (k Kernel) Interfaces() []string {
	links := k.links()
	var out []string
	for _, r := range k.Routes {
		if r.Table != mainTable || r.Type != unicast || r.Dst.Bits() != 0 {
			continue
		}
		l, ok := links[r.Index]
		if ok && !l.Loopback && !l.tunnel() && !slices.Contains(out, l.Name) {
			out = append(out, l.Name)
		}
	}
	return out
}

// Uplinks are the interfaces with a unicast default route in any table, the LAN's first: a VPN's tunnel too, which
// is never the LAN but is the way to the internet while it is up. Not loopback.
func (k Kernel) Uplinks() []string {
	links := k.links()
	out := k.Interfaces()
	for _, r := range k.Routes {
		if r.Type != unicast || r.Dst.Bits() != 0 {
			continue
		}
		if l, ok := links[r.Index]; ok && !l.Loopback && !slices.Contains(out, l.Name) {
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
// Never the mesh's ranges (meshRanges, and every network on a tunnel, #108): a prefix inside one is refused, so a
// rogue router advertising the mesh's ULA /64 on the LAN doesn't make mesh peers the LAN; a wider one keeps the rest
// of the network, and LAN.Excluded carves the range out. An address in one is never the LAN's.
//
// Its addresses are the machine's own on those networks, the best first: stable before temporary (privacy) ones, and
// none that is still tentative or failed duplicate address detection.
func (k Kernel) LAN() LAN {
	l := LAN{Interfaces: k.Interfaces()}
	onLink := k.onLinkPrefixes()
	mesh := k.meshPrefixes()
	inMesh := func(p netip.Prefix) bool {
		return slices.ContainsFunc(mesh, func(m netip.Prefix) bool { return m.Bits() <= p.Bits() && m.Contains(p.Addr()) })
	}
	var later []netip.Addr // temporary addresses, after the stable ones
	for _, name := range l.Interfaces {
		var v4, ula, global []lanAddr
		for _, a := range k.addrsOf(name) {
			switch addr := a.Prefix.Addr(); {
			case addr.Is4() && addr.IsPrivate():
				v4 = append(v4, lanAddr{Addr: a, lan: []netip.Prefix{a.Prefix.Masked()}})
			case addr.Is4():
			case addr.IsPrivate():
				ula = append(ula, lanAddr{Addr: a, lan: []netip.Prefix{a.Prefix.Masked()}})
			case addr.IsGlobalUnicast():
				var in []netip.Prefix
				for _, p := range onLink[name] {
					if p.Contains(addr) {
						in = append(in, p)
					}
				}
				global = append(global, lanAddr{Addr: a, lan: in})
			}
		}
		for _, a := range slices.Concat(v4, ula, global) {
			if a.lan = slices.DeleteFunc(a.lan, inMesh); len(a.lan) == 0 {
				continue
			}
			for _, p := range a.lan {
				if !slices.Contains(l.Prefixes, p) {
					l.Prefixes = append(l.Prefixes, p)
				}
			}
			switch {
			case inMesh(netip.PrefixFrom(a.Prefix.Addr(), a.Prefix.Addr().BitLen())):
			case a.Flags&(ifaTentative|ifaDADFailed) != 0:
			case a.Flags&(ifaTemporary|ifaDeprecated) != 0 && a.Prefix.Addr().Is6(): // IPv4's 0x01 is "secondary"
				later = append(later, a.Prefix.Addr())
			default:
				l.Addresses = append(l.Addresses, a.Prefix.Addr())
			}
		}
	}
	l.Addresses = append(l.Addresses, later...)
	for _, m := range mesh {
		if slices.ContainsFunc(l.Prefixes, m.Overlaps) && !slices.Contains(l.Excluded, m) {
			l.Excluded = append(l.Excluded, m)
		}
	}
	return l
}

// OnLink is what the firewall table keeps apps away from: the LAN's interfaces, and every prefix on them, private or
// global (a LAN may use global IPv6 addresses), but link-local, which the table covers whole. It is a list of what
// apps never reach, so nothing is left out of it, the mesh's ranges included.
func (k Kernel) OnLink() (interfaces []string, prefixes []netip.Prefix) {
	interfaces = k.Interfaces()
	for _, name := range interfaces {
		for _, a := range k.addrsOf(name) {
			p := a.Prefix.Masked()
			if !p.Addr().IsLinkLocalUnicast() && !p.Addr().IsLoopback() && !slices.Contains(prefixes, p) {
				prefixes = append(prefixes, p)
			}
		}
	}
	return interfaces, prefixes
}

func (l Link) tunnel() bool { return l.PointToPoint || slices.Contains(tunnelKinds, l.Kind) }

// links are the links by their index.
func (k Kernel) links() map[int]Link {
	m := make(map[int]Link, len(k.Links))
	for _, l := range k.Links {
		m[l.Index] = l
	}
	return m
}

func (k Kernel) addrsOf(name string) []Addr {
	links := k.links()
	var out []Addr
	for _, a := range k.Addrs {
		if l, ok := links[a.Index]; ok && l.Name == name {
			out = append(out, a)
		}
	}
	return out
}

// meshPrefixes are the ranges that are never the LAN: the meshes' own, and every network on a tunnel (the mesh's
// interface, a VPN's), its own end's and its peer's, but link-local, which every interface has.
func (k Kernel) meshPrefixes() []netip.Prefix {
	links := k.links()
	out := slices.Clone(meshRanges)
	for _, a := range k.Addrs {
		if l, ok := links[a.Index]; !ok || !l.tunnel() {
			continue
		}
		for _, p := range []netip.Prefix{a.Prefix, a.Peer} {
			if p.IsValid() && !p.Addr().IsLinkLocalUnicast() && !slices.Contains(out, p.Masked()) {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

// onLinkPrefixes are the IPv6 prefixes on the link, per interface, from the main table: unicast routes from /64 to
// /127 with no router in between. Not the default route (::/0) or any route wider than a /64, not the machine's own
// /128s, which the local table lists, and nothing another table holds.
func (k Kernel) onLinkPrefixes() map[string][]netip.Prefix {
	links := k.links()
	out := map[string][]netip.Prefix{}
	for _, r := range k.Routes {
		if r.Table != mainTable || r.Type != unicast || r.Gateway || !r.Dst.Addr().Is6() || r.Dst.Bits() < 64 || r.Dst.Bits() > 127 {
			continue
		}
		if l, ok := links[r.Index]; ok {
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

// lanAddr is an address with the LAN's prefixes that hold it.
type lanAddr struct {
	Addr
	lan []netip.Prefix
}

// Cached remembers what read returns for ttl, so a page's many requests don't each ask the kernel.
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
