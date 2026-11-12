// Package lan says what this machine's home network is: the private networks on the interfaces that carry its
// default route in the main routing table, and the global IPv6 prefixes on those links, as the kernel reports them
// over netlink. Only a link that may be a home network is the LAN: a NIC, or a VLAN, bond or bridge on one (#169).
// Containers' bridges carry no default route, and a VPN's or the mesh's tunnel is never the LAN, nor is any range of
// theirs (#108).
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
	// Master is the index of the bridge, bond or team this link is a port of (IFLA_MASTER), or 0.
	Master int
	// Lower is the index of the link a VLAN sits on (IFLA_LINK), or 0: none, or one in another namespace.
	Lower int
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

// localKinds are the kinds of link that are this machine's own and carry no tunnel: a physical NIC's (none), a
// bridge's, a container's veth, a VLAN, a bond, a team, a dummy, a macvlan, ipvlan or macvtap on a NIC, a VRF, Cilium's
// netkit, and the kernel's ifb and nlmon. Every other kind is a tunnel (#169): one that carries a VPN or a mesh
// (WireGuard, tun and tap, GRE, VXLAN, batman-adv, L2TP, bareudp, ERSPAN, AMT, ...), or one this code doesn't know.
var localKinds = []string{"", "bridge", "veth", "vlan", "bond", "team", "dummy", "macvlan", "ipvlan", "macvtap", "vrf",
	"netkit", "ifb", "nlmon"}

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

// Network is what the firewall table needs of this machine's network, from one reading of the kernel, so its parts
// agree: the LAN's interfaces and every prefix on them (Kernel.OnLink), and the uplinks of each address family.
type Network struct {
	LAN      []string
	Prefixes []netip.Prefix
	// Uplinks4 and Uplinks6 are the ways to the IPv4 and the IPv6 internet (Kernel.Uplinks).
	Uplinks4, Uplinks6 []string
	// Virtual are the bridges that may be a local virtual network or an app network (Kernel.Virtual).
	Virtual []string
}

// ReadNetwork is the Network, read from the live machine; an error, and no Network, when the kernel can't be asked.
// The caller decides what holds until it answers: never an empty LAN taken for the real one (#148).
func ReadNetwork() (Network, error) {
	k, err := ReadKernel()
	if err != nil {
		return Network{}, err
	}
	return k.Network(), nil
}

// Network is the Network this kernel describes.
func (k Kernel) Network() Network {
	var n Network
	n.Uplinks4, n.Uplinks6 = k.Uplinks()
	n.LAN, n.Prefixes = k.OnLink()
	n.Virtual = k.Virtual()
	return n
}

// Virtual are the bridges that may be a local virtual network or an app network, a container engine's (#169): those
// whose every port is a container's veth or a VM's tap, or that have none yet. A bridge with a NIC among its ports (a
// second NIC enslaved to it, a bond of NICs, a VLAN on one), an L2 tunnel or a link of a kind this code doesn't know
// reaches past this machine: it is none, and the firewall treats it as it treats that NIC.
func (k Kernel) Virtual() []string {
	ports := k.ports()
	var out []string
	for _, l := range k.Links {
		if l.Kind == "bridge" && !l.Loopback && !slices.ContainsFunc(ports[l.Index], func(p Link) bool { return !p.guest() }) {
			out = append(out, l.Name)
		}
	}
	return out
}

// Interfaces are the LAN's interfaces: those that carry a default route, IPv4 or IPv6, in the main table (#108), and
// may be a home network (a NIC, or a VLAN, bond or bridge on one: #169). Not loopback, and not a VPN's or the mesh's,
// even when a full-tunnel VPN puts its default route in the main table; and never through another table, where
// wg-quick puts a full tunnel's (51820).
func (k Kernel) Interfaces() []string { return k.defaults(func(netip.Prefix) bool { return true }) }

// defaults are the LAN's interfaces that carry a main-table default route that want accepts, in the kernel's order.
func (k Kernel) defaults(want func(netip.Prefix) bool) []string {
	links, ports := k.links(), k.ports()
	var out []string
	for _, r := range k.Routes {
		if r.Table != mainTable || r.Type != unicast || r.Dst.Bits() != 0 || !want(r.Dst) {
			continue
		}
		l, ok := links[r.Index]
		if ok && k.home(l, links, ports, 0) && !slices.Contains(out, l.Name) {
			out = append(out, l.Name)
		}
	}
	return out
}

func is4(p netip.Prefix) bool { return p.Addr().Is4() }
func is6(p netip.Prefix) bool { return p.Addr().Is6() }

// Uplinks are the ways to the internet, for each address family (#169): the LAN's interfaces that carry that family's
// default route first, then every tunnel with a default route of that family in any table (a VPN's, which is never the
// LAN). A tunnel's default route is a unicast one to the whole address space, or routes of its own, in one table, that
// cover the family's internet together (#148): OpenVPN's def1 puts 0.0.0.0/1 and 128.0.0.0/1 there instead, its IPv6
// redirect ::/3, 2000::/4, 3000::/4 and fc00::/7, and a WireGuard client often everything but the private ranges. A
// tunnel whose routes cover only the IPv6 internet is no way out for IPv4, whatever IPv4 ranges it also carries (a
// split tunnel's), and the other way round. Never another link: a local virtual network's bridge with a default route
// in a table of its own is still a local virtual network, whose VMs the firewall forwards.
func (k Kernel) Uplinks() (v4, v6 []string) {
	links := k.links()
	v4, v6 = k.defaults(is4), k.defaults(is6)
	// A tunnel's unicast routes, by table, interface and family, in the order the kernel lists them.
	type way struct {
		table uint32
		index int
		v4    bool
	}
	var ways []way
	dsts := map[way][]netip.Prefix{}
	for _, r := range k.Routes {
		if l, ok := links[r.Index]; !ok || r.Type != unicast || !l.tunnel() || l.Loopback {
			continue
		}
		w := way{r.Table, r.Index, r.Dst.Addr().Is4()}
		if _, ok := dsts[w]; !ok {
			ways = append(ways, w)
		}
		dsts[w] = append(dsts[w], r.Dst.Masked())
	}
	for _, w := range ways {
		internet, notInternet, out := internet6, []netip.Prefix(nil), &v6
		if w.v4 {
			internet, notInternet, out = internet4, notInternet4, &v4
		}
		if name := links[w.index].Name; !slices.Contains(*out, name) && covered(internet, slices.Concat(dsts[w], notInternet)) {
			*out = append(*out, name)
		}
	}
	return v4, v6
}

// The internet, as a tunnel's routes must cover it to be its default route: IPv4's space, of which notInternet4 is
// never the internet (this network, private and shared ranges, loopback, link-local, multicast and the reserved
// space: the firewall keeps apps from the private, shared and link-local ones, and the others are no public unicast
// destination), and IPv6's global unicast space.
var (
	internet4    = netip.MustParsePrefix("0.0.0.0/0")
	internet6    = netip.MustParsePrefix("2000::/3")
	notInternet4 = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("224.0.0.0/3")}
)

// covered says whether the prefixes in set cover all of target: in one pass over them, sorted, so a tunnel's
// thousands of routes (a BGP feed) cost little.
func covered(target netip.Prefix, set []netip.Prefix) bool {
	type span struct{ from, to netip.Addr }
	var spans []span
	for _, p := range set {
		if p.Addr().Is4() == target.Addr().Is4() && p.Overlaps(target) {
			spans = append(spans, span{p.Masked().Addr(), last(p)})
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.from.Compare(b.from) })
	next, end := target.Masked().Addr(), last(target)
	for _, s := range spans {
		switch {
		case s.from.Compare(next) > 0: // a gap before s
			return false
		case s.to.Compare(end) >= 0:
			return true
		case s.to.Compare(next) >= 0:
			next = s.to.Next() // s.to is below end: never the family's last address
		}
	}
	return false
}

// last is a prefix's last address.
func last(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
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

func (l Link) tunnel() bool { return l.PointToPoint || !slices.Contains(localKinds, l.Kind) }

// home says whether a link may be a home network, the LAN (#169): an allow-list. A physical NIC (a link of no kind,
// not point-to-point), or, on one: a VLAN, a bond or a team of them, or a bridge with one among its ports, whose other
// ports are only containers' veths and VMs' taps (a VM host's br0). Nothing else: a tunnel of any kind, a kind this
// code doesn't know, a veth, a dummy, a macvlan; nor a bridge of guests only, which is a local virtual network.
func (k Kernel) home(l Link, links map[int]Link, ports map[int][]Link, depth int) bool {
	if depth > 8 || l.Loopback || l.PointToPoint {
		return false
	}
	switch l.Kind {
	case "":
		return true
	case "vlan":
		lower, ok := links[l.Lower]
		return l.Lower != 0 && lower.Index != l.Index && ok && k.home(lower, links, ports, depth+1)
	case "bond", "team":
		return len(ports[l.Index]) > 0 && !slices.ContainsFunc(ports[l.Index], func(p Link) bool {
			return !k.home(p, links, ports, depth+1)
		})
	case "bridge":
		nic := false
		for _, p := range ports[l.Index] {
			switch {
			case k.home(p, links, ports, depth+1):
				nic = true
			case !p.guest():
				return false
			}
		}
		return nic
	}
	return false
}

// guest says whether a bridge's port is a container's or a VM's: a veth, or a tap (the tun driver's kind, never
// point-to-point: a tun device can't be a bridge's port).
func (l Link) guest() bool { return l.Kind == "veth" || l.Kind == "tun" && !l.PointToPoint }

// ports are the links by the index of the bridge, bond or team they are ports of.
func (k Kernel) ports() map[int][]Link {
	m := map[int][]Link{}
	for _, l := range k.Links {
		if l.Master != 0 {
			m[l.Master] = append(m[l.Master], l)
		}
	}
	return m
}

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
