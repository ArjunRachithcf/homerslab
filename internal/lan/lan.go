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
	"strings"
	"sync"
	"time"
)

// FromHere says whether a request from addr comes from this machine or one of the networks in lans.
func FromHere(addr netip.Addr, lans []netip.Prefix) bool {
	addr = addr.Unmap().WithZone("") // a link-local address carries its interface, which prefixes never match
	if addr.IsLoopback() {
		return true
	}
	for _, p := range lans {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Networks are the LAN's networks, read from the live machine.
func Networks() []netip.Prefix {
	var out []netip.Prefix
	for _, name := range DefaultRouteInterfaces(os.DirFS("/proc")) {
		for _, p := range addrs(name) {
			if private(p.Addr()) {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

// Address is this machine's IPv4 address on the LAN, for the links it prints; or the zero Addr if it has none.
func Address() netip.Addr {
	for _, name := range DefaultRouteInterfaces(os.DirFS("/proc")) {
		for _, p := range addrs(name) {
			if a := p.Addr(); a.Is4() && private(a) {
				return a
			}
		}
	}
	return netip.Addr{}
}

func private(a netip.Addr) bool { return a.IsPrivate() || a.Is6() && a.IsLinkLocalUnicast() }

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

// Cached remembers what networks returns for ttl, so a page's many requests don't each read /proc and the interfaces.
func Cached(networks func() []netip.Prefix, ttl time.Duration) func() []netip.Prefix {
	var mu sync.Mutex
	var at time.Time
	var last []netip.Prefix
	return func() []netip.Prefix {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > ttl {
			last, at = networks(), time.Now()
		}
		return last
	}
}
