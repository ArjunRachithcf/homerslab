// Package firewall is the agent's firewall table: one nftables table, inet homerslab, that keeps the LAN away from
// everything but the machine itself, and keeps apps away from the LAN and the mesh (spec: the agent owns the firewall
// table; story 73; #82).
//
// It knows four kinds of interface, each by its exact name: loopback, the mesh (NetBird's wt0), the container
// engines' bridges (as Docker and Podman list them), and the LAN (the interfaces that carry the default route).
// Everything else is a local virtual network (libvirt's virbr0, LXD's lxdbr0, a bridge nobody declared): its VMs
// keep the internet, and nothing more. A name proves nothing: a LAN bridge called br-iot is not a container bridge,
// and while no engine answers (at boot) no bridge is trusted.
//
//   - From the LAN: the machine itself only, by ICMP (ping, IPv6 neighbour discovery), DHCP and mDNS replies, the
//     mesh's WireGuard port (so peers at home connect directly), and the setup link's listener while it is open,
//     until the admin's first sign-in over the mesh closes it. No listener, no published port (DNAT to a container
//     is forwarding, and dropped), and nothing routed.
//   - From the mesh: any listener of this machine (the mesh's own policies filter it), but nothing routed: only the
//     edge, on this machine, reaches an app (#19).
//   - From the apps (the engines' bridges): this machine's DNS, each other's bridges, and the internet; never the
//     LAN, the mesh, a private range or a local virtual network. Extras that let an app reach the LAN or the mesh
//     come with the install plan (#19, #82).
//   - From a local virtual network: no listener, and the internet only.
//
// A drop in any base chain is final in nftables, whatever other tables (firewalld, Docker's) accept, so this table
// holds whatever else the machine runs. It changes no other table.
package firewall

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

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
	// Bridges are the container engines' bridges, by their exact names, as the engines list them. A LAN interface
	// is never one, whatever the engines say.
	Bridges []string `json:"bridges"`
}

// Equal says whether two states make the same table.
func (s State) Equal(o State) bool { return Render(s) == Render(o) }

// Render is the nftables script that replaces the table with s, atomically: one transaction, so no packet ever meets
// a half-made table.
func Render(s State) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	lan := uniqStrings(s.LAN)
	mesh := uniqStrings(s.Mesh)
	bridges := slices.DeleteFunc(uniqStrings(s.Bridges), func(n string) bool { return slices.Contains(lan, n) || slices.Contains(mesh, n) })
	w("# HomersLab's agent owns this table, and puts it back when it drifts.")
	w("table %s", Table)
	w("delete table %s", Table)
	w("table %s {", Table)

	w("\tchain input {")
	w("\t\ttype filter hook input priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t\tct state invalid drop")
	w("\t\tiif \"lo\" accept")
	if len(mesh) > 0 {
		w("\t\tiifname %s accept", set(quoted(mesh)))
	}
	if len(bridges) > 0 {
		w("\t\tiifname %s jump apps_in", set(quoted(bridges)))
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
	if len(bridges) > 0 {
		w("\t\tiifname %s jump apps_out", set(quoted(bridges)))
	}
	if len(lan) > 0 {
		w("\t\tiifname %s drop", set(quoted(lan)))
	}
	w("\t\tjump outbound")
	w("\t}")

	// Apps to this machine: only the DNS an engine serves on a bridge's gateway.
	w("\tchain apps_in {")
	w("\t\tmeta l4proto { tcp, udp } th dport 53 accept")
	w("\t\tdrop")
	w("\t}")
	w("\tchain apps_out {")
	if len(bridges) > 0 {
		w("\t\toifname %s accept", set(quoted(bridges)))
	}
	w("\t\tjump outbound")
	w("\t}")
	// To the internet only: out through the LAN's uplink, to an address that is neither private nor the LAN's.
	w("\tchain outbound {")
	if len(lan) > 0 {
		w("\t\toifname != %s drop", set(quoted(lan)))
	} else {
		w("\t\tdrop")
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
	listing, err := f.list(ctx)
	if err != nil {
		return err
	}
	f.listing, f.checked = listing, f.now()
	return nil
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
