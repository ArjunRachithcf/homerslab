// Package firewall is the agent's firewall table: one nftables table, inet homerslab, that keeps the LAN away from
// everything but the machine itself, and keeps apps away from the LAN and the mesh (spec: the agent owns the firewall
// table; story 73; #82).
//
// It knows four kinds of interface, each by its exact name: loopback, the mesh (NetBird's wt0), the container
// engines' bridges (as Docker and Podman list them, and bridges to the kernel too), and the LAN (the interfaces that
// carry the default route). Any other bridge whose ports are all containers' veths or VMs' taps, as the kernel last
// said, is a local virtual network (libvirt's virbr0, LXD's lxdbr0, a bridge nobody declared): its VMs keep the
// internet, and nothing more. Every other interface (a tunnel, a VPN's with a default route or without, one the agent
// hasn't seen yet; a second NIC, or a bridge with one among its ports) gets the machine's basics and nothing
// forwarded: no more than the LAN (#148, #169); an engine's bridge with a NIC gets nothing in at all. Apps leave for
// each address family through that family's uplinks only. A name proves nothing: a LAN bridge called br-iot is not a
// container bridge, and while no engine answers (at boot) no bridge is trusted.
//
//   - From the LAN: the machine itself only, by ICMP (ping, IPv6 neighbour discovery), DHCP and mDNS replies, the
//     mesh's WireGuard port (so peers at home connect directly), and the setup link's listener while it is open,
//     until the admin's first sign-in over the mesh closes it. No listener, no published port (DNAT to a container
//     is forwarding, and dropped), and nothing routed.
//   - From the mesh: any listener of this machine (the mesh's own policies filter it), but nothing routed: only the
//     edge, on this machine, reaches an app (#19).
//   - From the apps (the engines' bridges): this machine's DNS at their bridge's own gateway, the sign-in listener (its
//     own address, on the household's port: ADR 0020), the resolvers it uses
//     out through an uplink (never one at a mesh peer), IPv6 neighbour discovery,
//     their own bridge, and the internet through an uplink of the packet's address family (the LAN's, or a VPN's
//     tunnel, which is never the LAN); never another app's bridge, the LAN, the mesh, a private range or a local
//     virtual network (ADR 0020). Extras that let an app reach the LAN or the mesh come with the install plan (#19).
//   - From a local virtual network: this machine's DHCP, and its DNS at the network's own gateway (libvirt's and LXD's
//     dnsmasq), no other listener, and the internet only.
//   - From a tunnel, or any other interface that isn't a bridge of guests (a second NIC, a bridge with one among its
//     ports): the machine's basics, as from the LAN, but never the setup listener, and nothing routed (#148, #169).
//
// An interface name is rendered only when it is a plain name an interface can have: a wildcard or a quote from an
// engine never reaches nft. The one wildcard is the table's own: the bridges the agent names for apps (hla*).
//
// A drop in any base chain is final in nftables, whatever other tables (firewalld, Docker's) accept, so this table
// holds whatever else the machine runs. It changes no other table.
package firewall

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// Table is the table's family and name.
const Table = "inet homerslab"

// Private are the ranges no app reaches: RFC 1918, CGNAT (where meshes live), link-local, IPv6 ULA and link-local.
var Private = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
}

// State is what the table lets in.
type State struct {
	// Mesh are the mesh interfaces (NetBird's wt0). Their own clients filter them by the mesh's policies.
	Mesh []string `json:"mesh"`
	// WireGuard are the mesh clients' UDP ports, open from anywhere so peers connect directly.
	WireGuard []int `json:"wireGuard"`
	// SetupPort is the setup link's LAN listener while it is open, or 0.
	SetupPort int `json:"setupPort"`
	// LAN are the interfaces that carry the default route, and their prefixes (a LAN may use global IPv6
	// addresses). With none (no default route yet), nothing is let in from outside but the machine's own basics.
	LAN         []string       `json:"lan"`
	LANPrefixes []netip.Prefix `json:"lanPrefixes"`
	// Uplinks4 and Uplinks6 are the ways to the IPv4 and the IPv6 internet: the LAN's interfaces that carry that
	// family's default route, and a VPN's tunnel whose routes cover that family's internet, which is never the LAN
	// (#108, #169). Apps reach each family's internet through its own uplinks only (none: no way out for that family);
	// nothing is forwarded from any of them. Never the mesh or an engine's bridge.
	Uplinks4 []string `json:"uplinks4,omitempty"`
	Uplinks6 []string `json:"uplinks6,omitempty"`
	// Bridges are the container engines' bridges, by their exact names, as the engines list them. A LAN interface
	// is never one, whatever the engines say.
	Bridges []string `json:"bridges"`
	// Virtual are the bridges whose every port is a container's veth or a VM's tap, or that have none, as the kernel
	// last said (#169): the only ones that may be a local virtual network or an app network. A bridge with a NIC among
	// its ports, or one not read yet, is neither: no virtual_in, nothing forwarded, as for that NIC; an engine's bridge
	// like that lets nothing in.
	Virtual []string `json:"virtual"`
	// Resolvers are the DNS servers this machine uses, which an engine hands its containers (Docker's default bridge
	// copies them): apps may ask those the LAN leads to, on port 53 only.
	Resolvers []netip.Addr `json:"resolvers"`
	// Upstreams are where this machine's own processes may open a connection into an app's network: each app's one
	// upstream, which the edge proxies to, and the sign-in's server. Only the users each names may (the edge's, the
	// agent's for its health probes, and the manager's for the sign-in); nothing else of this machine opens a
	// connection into an app's network (ADR 0020, #82).
	Upstreams []Upstream `json:"upstreams,omitempty"`
	// SignInListener is where apps reach the sign-in on this node (ADR 0020): the sign-in listener's own address, on
	// the household's port (and LDAPS's 636 once an LDAP app is installed, #204). The one thing of this machine's an app
	// may open besides its engine's DNS.
	SignInListener []netip.AddrPort `json:"signInListener,omitempty"`
}

// Upstream is one address and TCP port on an app's network, and the users of this machine that may connect to it.
type Upstream struct {
	Addr netip.AddrPort `json:"addr"`
	UIDs []uint32       `json:"uids"`
}

// ValidName says whether an interface name is one nft may be given: Linux's limit of 15 characters, and none that
// nft would read as a wildcard or a quote.
func ValidName(n string) bool {
	if n == "" || len(n) > 15 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func valid(names []string) []string {
	return slices.DeleteFunc(uniqStrings(names), func(n string) bool { return !ValidName(n) })
}

// Equal says whether two states make the same table.
func (s State) Equal(o State) bool { return Render(s) == Render(o) }

// header opens every table Render writes: the table made (if missing), deleted and made again, in one transaction.
const header = "# HomersLab's agent owns this table, and puts it back when it drifts.\n" +
	"table " + Table + "\n" +
	"delete table " + Table + "\n" +
	"table " + Table + " {\n"

// Render is the nftables script that replaces the table with s, atomically: one transaction, so no packet ever meets
// a half-made table.
func Render(s State) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	lan := valid(s.LAN)
	mesh := valid(s.Mesh)
	bridges := slices.DeleteFunc(valid(s.Bridges), func(n string) bool { return slices.Contains(lan, n) || slices.Contains(mesh, n) })
	// The ways out, for each family; never the mesh or an engine's bridge unless it is the LAN's.
	wayOut := func(uplinks []string) []string {
		return slices.DeleteFunc(valid(uplinks), func(n string) bool {
			return !slices.Contains(lan, n) && (slices.Contains(mesh, n) || slices.Contains(bridges, n))
		})
	}
	out4, out6 := wayOut(s.Uplinks4), wayOut(s.Uplinks6)
	// The LAN and every uplink: never a local virtual network, and nothing forwarded from them.
	out := uniqStrings(slices.Concat(lan, out4, out6))
	// The bridges that may be an app network or a local virtual network: guests' ports only (#169).
	guests := valid(s.Virtual)
	var apps, notApps []string
	for _, br := range bridges {
		if slices.Contains(guests, br) {
			apps = append(apps, br)
		} else {
			notApps = append(notApps, br)
		}
	}
	virtual := slices.DeleteFunc(guests, func(n string) bool {
		return slices.Contains(out, n) || slices.Contains(mesh, n) || slices.Contains(bridges, n)
	})
	b.WriteString(header)

	w("\tchain input {")
	w("\t\ttype filter hook input priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t\tct state invalid drop")
	w("\t\tiif \"lo\" accept")
	if len(mesh) > 0 {
		w("\t\tiifname %s accept", set(quoted(mesh)))
	}
	if len(apps) > 0 {
		// An engine's bridge is a bridge to the kernel too (#148): a tunnel an engine names is no app network. And one
		// of guests' ports only (#169).
		w("\t\tiifname %s meta iifkind \"bridge\" jump apps_in", set(quoted(apps)))
	}
	if len(notApps) > 0 {
		// An engine's bridge with a NIC among its ports, or one not read yet: nothing in, neither as an app network
		// nor as the NIC (#169).
		w("\t\tiifname %s meta iifkind \"bridge\" drop", set(quoted(notApps)))
	}
	if len(lan) > 0 && len(virtual) > 0 {
		// A local virtual network is a bridge of guests' ports only (Virtual) that is neither the LAN, an uplink, an
		// engine's (above) nor the mesh. The kernel's kind of link decides too, so a tunnel (a VPN's, an L2 one, one
		// the agent hasn't seen yet) never is one, nor an interface of no kind (a second NIC), nor a bridge with a NIC
		// among its ports: they get the machine's basics below, as the LAN does (#148, #169). The guard is the LAN,
		// not the ways out: while no LAN is known (no default route in the main table, or a VPN that replaced it), the
		// LAN may itself be a bridge (a VM host's br0), and no bridge is taken for a local virtual network.
		w("\t\tiifname %s meta iifkind \"bridge\" jump virtual_in", set(quoted(virtual)))
	}
	w("\t\tmeta l4proto { icmp, ipv6-icmp } accept")
	w("\t\tudp dport { 68, 546, 5353 } accept")
	for _, p := range uniq(s.WireGuard) {
		w("\t\tudp dport %d accept", p)
	}
	if s.SetupPort > 0 && len(lan) > 0 {
		w("\t\tiifname %s tcp dport %d accept", set(quoted(lan)), s.SetupPort)
	}
	w("\t}")

	w("\tchain forward {")
	w("\t\ttype filter hook forward priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	if len(mesh) > 0 {
		w("\t\tiifname %s drop", set(quoted(mesh)))
	}
	if len(apps) > 0 {
		w("\t\tiifname %s meta iifkind \"bridge\" jump apps_out", set(quoted(apps)))
	}
	if len(out) > 0 {
		w("\t\tiifname %s drop", set(quoted(out)))
	}
	// Only a local virtual network, a bridge of guests' ports, is forwarded to the internet: never a tunnel, with a
	// default route or without, nor an interface of no kind (a match on a kind that a link hasn't fails, so it is the
	// policy's drop), nor a bridge with a NIC among its ports (#169). And only while the LAN is known, as virtual_in:
	// otherwise the LAN's own bridge would be forwarded, through a VPN that replaced the main table's default route,
	// say (#167's review).
	if len(lan) > 0 && len(virtual) > 0 {
		w("\t\tiifname %s meta iifkind \"bridge\" jump outbound", set(quoted(virtual)))
	}
	w("\t}")

	w("\tchain output {")
	w("\t\ttype filter hook output priority filter - 5; policy accept;")
	// The agent names every bridge of an app's (engine.BridgePrefix), so a new app's networks are kept to the edge from
	// the moment the engine makes them, before the engines list them (#19's review). Ours, never an engine's: a wildcard.
	w("\t\toifname \"%s*\" jump to_apps", engine.BridgePrefix)
	if len(apps) > 0 {
		w("\t\toifname %s jump to_apps", set(quoted(apps)))
	}
	if addrs := upstreamAddrs(s.Upstreams); len(addrs) > 0 {
		w("\t\tip daddr . meta l4proto . th dport %s jump to_apps", set(addrs))
	}
	w("\t}")

	// Apps to this machine: only the DNS an engine serves on a bridge's gateway (an address of that bridge itself, never
	// the mesh's, the LAN's or another bridge's), and IPv6's neighbour discovery.
	w("\tchain apps_in {")
	w("\t\tmeta l4proto { tcp, udp } th dport 53 fib daddr . iif type local accept")
	if l := listenerAddrs(s.SignInListener); len(l) > 0 {
		w("\t\tip daddr . tcp dport %s accept", set(l))
	}
	w("\t\ticmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit } accept")
	w("\t\tdrop")
	w("\t}")
	// Local virtual networks to this machine: their DHCP and their DNS (dnsmasq, on the network's own gateway; never
	// the mesh's resolver or another address of the machine), then the machine's basics.
	w("\tchain virtual_in {")
	w("\t\tudp dport { 67, 547 } accept")
	w("\t\tmeta l4proto { tcp, udp } th dport 53 fib daddr . iif type local accept")
	// Belt and braces: the input chain's policy drops these too, but port 53 is decided here, whatever is added below.
	w("\t\tmeta l4proto { tcp, udp } th dport 53 drop")
	w("\t}")
	// Apps out: their own bridge, the machine's resolvers through an uplink (the one DHCP gave it, which Docker's default
	// bridge copies: on purpose; never one at a mesh peer), the internet.
	w("\tchain apps_out {")
	for _, br := range apps {
		w("\t\tiifname %q oifname %q accept", br, br)
	}
	var r4, r6 []string
	for _, r := range s.Resolvers {
		switch {
		case r.IsLoopback() || r.IsUnspecified():
		case r.Unmap().Is4():
			r4 = append(r4, r.Unmap().String())
		default:
			r6 = append(r6, r.String())
		}
	}
	// A resolver is reached through the LAN, where DHCP or a router's advertisement found it even with no default route
	// of its family (#169's review), or through an uplink of its family; never through a tunnel that isn't one.
	if to4 := uniqStrings(slices.Concat(lan, out4)); len(r4) > 0 && len(to4) > 0 {
		w("\t\toifname %s ip daddr %s meta l4proto { tcp, udp } th dport 53 accept", set(quoted(to4)), set(uniqStrings(r4)))
	}
	if to6 := uniqStrings(slices.Concat(lan, out6)); len(r6) > 0 && len(to6) > 0 {
		w("\t\toifname %s ip6 daddr %s meta l4proto { tcp, udp } th dport 53 accept", set(quoted(to6)), set(uniqStrings(r6)))
	}
	w("\t\tjump outbound")
	w("\t}")
	// This machine's own processes into the apps' networks (#82): only to an upstream, only as one of its users, so a
	// local process can't reach an app around the edge and hand it a forged identity header. By the apps' bridges,
	// and by the upstreams' addresses on whatever interface leads to them. Replies, and ping, pass.
	w("\tchain to_apps {")
	w("\t\tct state established,related accept")
	w("\t\tmeta l4proto { icmp, ipv6-icmp } accept")
	if allowed := upstreamUsers(s.Upstreams); len(allowed) > 0 {
		w("\t\tip daddr . tcp dport . meta skuid %s accept", set(allowed))
	}
	w("\t\tdrop")
	w("\t}")
	// To the internet only: out through an uplink of the packet's family (the LAN's, a VPN's), to an address that is
	// neither private nor the LAN's. A family with no uplink has no way out.
	w("\tchain outbound {")
	switch {
	case len(out4) == 0 && len(out6) == 0:
		w("\t\tdrop")
	default:
		for _, f := range []struct {
			family string
			out    []string
		}{{"ipv4", out4}, {"ipv6", out6}} {
			if len(f.out) > 0 {
				w("\t\tmeta nfproto %s oifname != %s drop", f.family, set(quoted(f.out)))
			} else {
				w("\t\tmeta nfproto %s drop", f.family)
			}
		}
	}
	var v4, v6 []string
	for _, p := range disjoint(append(slices.Clone(Private), s.LANPrefixes...)) {
		if p.Addr().Is4() {
			v4 = append(v4, p.Masked().String())
		} else {
			v6 = append(v6, p.Masked().String())
		}
	}
	w("\t\tip daddr %s drop", set(uniqStrings(v4)))
	w("\t\tip6 daddr %s drop", set(uniqStrings(v6)))
	w("\t\taccept")
	w("\t}")
	w("}")
	return b.String()
}

// listenerAddrs are the sign-in listener's addresses as nft's address . port, in order: IPv4, with a port.
func listenerAddrs(l []netip.AddrPort) []string {
	var out []string
	for _, a := range l {
		if a.Addr().Unmap().Is4() && a.Port() != 0 {
			out = append(out, fmt.Sprintf("%s . %d", a.Addr().Unmap(), a.Port()))
		}
	}
	return uniqStrings(out)
}

// usable are the upstreams that can be rendered: IPv4 with a port, and at least one user.
func usable(ups []Upstream) []Upstream {
	return slices.DeleteFunc(slices.Clone(ups), func(u Upstream) bool {
		return !u.Addr.Addr().Unmap().Is4() || u.Addr.Port() == 0 || len(u.UIDs) == 0
	})
}

// upstreamAddrs are the upstreams as nft's address . protocol . port, in order.
func upstreamAddrs(ups []Upstream) []string {
	var out []string
	for _, u := range usable(ups) {
		out = append(out, fmt.Sprintf("%s . tcp . %d", u.Addr.Addr().Unmap(), u.Addr.Port()))
	}
	return uniqSorted(out, upstreamOrder)
}

// upstreamUsers are the upstreams with each of their users, as nft's address . port . uid, in order.
func upstreamUsers(ups []Upstream) []string {
	var out []string
	for _, u := range usable(ups) {
		for _, uid := range u.UIDs {
			out = append(out, fmt.Sprintf("%s . %d . %d", u.Addr.Addr().Unmap(), u.Addr.Port(), uid))
		}
	}
	return uniqSorted(out, upstreamOrder)
}

// upstreamOrder sorts nft's concatenations by their address, then numerically by each part after it.
func upstreamOrder(a, b string) int {
	pa, pb := strings.Split(a, " . "), strings.Split(b, " . ")
	if c := netip.MustParseAddr(pa[0]).Compare(netip.MustParseAddr(pb[0])); c != 0 {
		return c
	}
	for i := 1; i < len(pa) && i < len(pb); i++ {
		x, errx := strconv.Atoi(pa[i])
		y, erry := strconv.Atoi(pb[i])
		switch {
		case errx == nil && erry == nil && x != y:
			return x - y
		case (errx != nil || erry != nil) && pa[i] != pb[i]:
			return strings.Compare(pa[i], pb[i])
		}
	}
	return len(pa) - len(pb)
}

func uniqSorted(s []string, cmp func(a, b string) int) []string {
	slices.SortFunc(s, cmp)
	return slices.Compact(s)
}

// disjoint drops every prefix another one already holds: nft refuses overlapping intervals in one set.
func disjoint(ps []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for i, p := range ps {
		p = p.Masked()
		held := false
		for j, q := range ps {
			q = q.Masked()
			if i != j && q.Bits() <= p.Bits() && q.Contains(p.Addr()) && (q.Bits() < p.Bits() || j < i) {
				held = true
				break
			}
		}
		if !held {
			out = append(out, p)
		}
	}
	return out
}

func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

func set(items []string) string { return "{ " + strings.Join(items, ", ") + " }" }

func uniqStrings(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

func uniq(ports []int) []int {
	out := slices.Clone(ports)
	slices.Sort(out)
	return slices.Compact(out)
}

// Drift is one time the table was found changed, and put back.
type Drift struct {
	At   time.Time `json:"at"`
	What string    `json:"what"` // missing, or changed
}

// Status is the table as the agent last found it.
type Status struct {
	Enforcing bool      `json:"enforcing"`
	State     State     `json:"state"`
	CheckedAt time.Time `json:"checkedAt"`
	Drift     []Drift   `json:"drift"`
}

// Firewall applies the table and keeps it as applied.
type Firewall struct {
	Run host.Runner
	Now func() time.Time
	// SavePath is where the table, once applied, is written (0600, atomically) for homerslab-firewall-restore, which
	// loads it at boot before the network comes up; "" for none.
	SavePath string

	mu      sync.Mutex
	on      bool
	state   State
	listing string // the table as nft listed it right after it was applied
	checked time.Time
	drift   []Drift
}

// keep is how many drift reports are remembered.
const keep = 100

// Apply makes the table s. From then on Check keeps it so.
func (f *Firewall) Apply(ctx context.Context, s State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.apply(ctx, s); err != nil {
		return err
	}
	f.on, f.state = true, s
	return nil
}

// Applied says whether the table was applied, and not lost since (as far as the last check knows).
func (f *Firewall) Applied() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on && f.listing != ""
}

// Want makes s the table Check keeps, without applying it now: for a table that couldn't be applied, so the next
// check tries again.
func (f *Firewall) Want(s State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on, f.state, f.listing = true, s, ""
}

func (f *Firewall) apply(ctx context.Context, s State) error {
	if _, err := f.Run(ctx, host.Cmd{Name: "nft", Args: []string{"-f", "-"}, Stdin: []byte(Render(s))}); err != nil {
		return fmt.Errorf("applying the firewall table: %w", err)
	}
	if f.SavePath != "" {
		if err := Save(f.SavePath, Render(s)); err != nil {
			return fmt.Errorf("saving the firewall table for the next boot: %w", err)
		}
	}
	listing, err := f.list(ctx)
	if err != nil {
		return err
	}
	f.listing, f.checked = listing, f.now()
	return nil
}

// Save writes a table's script to path, atomically, for root only, and synced to the disk before it returns.
func Save(path, script string) error { return atomicfile.Write(path, []byte(script)) }

// Closed is the table for a machine whose agent has applied none yet (its first boot): loopback, the machine's
// basics (ICMP, DHCP and mDNS replies) and the setup listener only, from anywhere, and nothing forwarded.
func Closed(setupPort int) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("# HomersLab's first-boot table: closed until the agent applies its own.")
	w("table %s", Table)
	w("delete table %s", Table)
	w("table %s {", Table)
	w("\tchain input {")
	w("\t\ttype filter hook input priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t\tct state invalid drop")
	w("\t\tiif \"lo\" accept")
	w("\t\tmeta l4proto { icmp, ipv6-icmp } accept")
	w("\t\tudp dport { 68, 546, 5353 } accept")
	if setupPort > 0 && setupPort < 65536 {
		w("\t\ttcp dport %d accept", setupPort)
	}
	w("\t}")
	w("\tchain forward {")
	w("\t\ttype filter hook forward priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t}")
	w("}")
	return b.String()
}

// RestoreScript is what homerslab-firewall-restore loads at boot: the table the agent last applied, or Closed when
// it never applied one. A saved file that can't be used (unreadable, cut short by a power cut, or not a table Render
// wrote) is never loaded: Closed is, and unusable says why.
func RestoreScript(savePath string, setupPort int) (script string, unusable error) {
	b, err := os.ReadFile(savePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Closed(setupPort), nil
	case err != nil:
		return Closed(setupPort), err
	case !whole(string(b)):
		return Closed(setupPort), fmt.Errorf("%s isn't a whole HomersLab firewall table", savePath)
	}
	return string(b), nil
}

// whole says whether a script is one Render wrote, all of it: its opening lines first, and the table's closing brace as
// its last line. Render writes exactly one brace alone on a line at column 0, that last one (every other line inside
// the table is indented), so a script cut short anywhere lacks it.
func whole(script string) bool {
	body, ok := strings.CutSuffix(script, "\n}\n")
	return ok && strings.HasPrefix(script, header) && !slices.Contains(strings.Split(body, "\n"), "}")
}

// Note records something wrong the agent found and couldn't render (an engine's bridge with a refused name).
func (f *Firewall) Note(what string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drift = append(f.drift, Drift{At: f.now(), What: what})
	if len(f.drift) > keep {
		f.drift = f.drift[len(f.drift)-keep:]
	}
}

var errMissing = errors.New("the table is missing")

// list is the table as nft lists it, without counters or other state.
func (f *Firewall) list(ctx context.Context) (string, error) {
	out, err := f.Run(ctx, host.Cmd{Name: "nft", Args: []string{"-s", "list", "table", "inet", "homerslab"}})
	if err != nil {
		var he *host.Error
		if errors.As(err, &he) && strings.Contains(he.Stderr, "No such file or directory") {
			return "", errMissing
		}
		return "", err
	}
	return string(out), nil
}

// Check compares the table with what was applied, and puts it back if it drifted. It returns the drift it found, if
// any.
func (f *Firewall) Check(ctx context.Context) (*Drift, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.on {
		return nil, nil
	}
	listing, err := f.list(ctx)
	what := "changed"
	switch {
	case errors.Is(err, errMissing):
		what = "missing"
	case err != nil:
		return nil, err
	case listing == f.listing:
		f.checked = f.now()
		return nil, nil
	}
	if err := f.apply(ctx, f.state); err != nil {
		return nil, err
	}
	d := Drift{At: f.now(), What: what}
	f.drift = append(f.drift, d)
	if len(f.drift) > keep {
		f.drift = f.drift[len(f.drift)-keep:]
	}
	return &d, nil
}

// Status is the table's state and its drift history.
func (f *Firewall) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{Enforcing: f.on, State: f.state, CheckedAt: f.checked, Drift: slices.Clone(f.drift)}
}

func (f *Firewall) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
