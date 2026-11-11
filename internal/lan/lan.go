// Package lan says what this machine's home network is: the private networks on the interfaces that carry its
// default route. Containers' bridges and the mesh carry no default route, so they are not the LAN.
package lan

import (
	"bufio"
	"bytes"
	"io/fs"
	"net"
	"net/netip"
	"os"
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

// Current is the LAN, read from the live machine.
func Current() LAN {
	l := LAN{Interfaces: DefaultRouteInterfaces(os.DirFS("/proc"))}
	for _, name := range l.Interfaces {
		for _, p := range addrs(name) {
			if private(p.Addr()) {
				l.Prefixes = append(l.Prefixes, p.Masked())
				l.Addresses = append(l.Addresses, p.Addr())
			}
		}
	}
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

// Address is this machine's IPv4 address on the LAN, for the links it prints; or the zero Addr if it has none.
func (l LAN) Address() netip.Addr {
	for _, a := range l.Addresses {
		if a.Is4() {
			return a
		}
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

// private is an address of a home network: RFC 1918 or an IPv6 ULA. Link-local addresses go by their zone instead.
func private(a netip.Addr) bool { return a.IsPrivate() }

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
