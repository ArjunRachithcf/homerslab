// Package firewall is the agent's firewall table: one nftables table, inet homerslab, that keeps the LAN away from
// everything but the machine itself, and keeps apps away from the LAN and the mesh (spec: the agent owns the firewall
// table; story 73; #82).
//
// Coming in, on every interface that is neither loopback, a mesh nor a container bridge (the LAN, and anything else
// plugged in):
//   - no published ports: nothing that arrives there is forwarded, so Docker's DNAT to a container is dropped too;
//   - listeners on the mesh only: no new connection reaches this machine, except
//   - the machine itself: ICMP (ping, IPv6 neighbour discovery), DHCP and mDNS replies, and the mesh's WireGuard
//     port, so peers at home connect directly;
//   - the setup link's LAN listener, while it is open, until the admin's first sign-in over the mesh closes it;
//   - nothing from the LAN is routed.
//
// Going out, from the container bridges (apps): the internet, and each other's bridges, but no new connection to the
// LAN's prefixes, private and link-local ranges, the mesh, or this machine itself except its DNS (the resolver an
// engine provides on a bridge's gateway). Extras that let an app reach the LAN or the mesh come with the install plan
// (#19, #82).
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

// Bridges are the interface names of container networks on this machine: Docker's and Podman's. Traffic from them is
// the machine's own containers.
var Bridges = []string{"docker0", "br-*", "podman*", "cni-podman*"}

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
	// addresses). A LAN interface is never taken for a container bridge, whatever its name (a bridge named br-lan).
	LAN         []string       `json:"lan"`
	LANPrefixes []netip.Prefix `json:"lanPrefixes"`
}

// Equal says whether two states make the same table.
func (s State) Equal(o State) bool { return Render(s) == Render(o) }

// Render is the nftables script that replaces the table with s, atomically: one transaction, so no packet ever meets
// a half-made table.
func Render(s State) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("# HomersLab's agent owns this table, and puts it back when it drifts.")
	w("table %s", Table)
	w("delete table %s", Table)
	w("table %s {", Table)
	notLAN := ""
	if len(s.LAN) > 0 {
		notLAN = " iifname != " + set(quoted(s.LAN))
	}
	inside := func(apps string) {
		w("\t\tiif \"lo\" accept")
		for _, m := range s.Mesh {
			w("\t\tiifname %q accept", m)
		}
		for _, br := range Bridges {
			w("\t\tiifname %q%s jump %s", br, notLAN, apps)
		}
	}
	w("\tchain input {")
	w("\t\ttype filter hook input priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t\tct state invalid drop")
	inside("apps_in")
	w("\t\tmeta l4proto { icmp, ipv6-icmp } accept")
	w("\t\tudp dport { 68, 546, 5353 } accept")
	for _, p := range uniq(s.WireGuard) {
		w("\t\tudp dport %d accept", p)
	}
	if s.SetupPort > 0 {
		w("\t\ttcp dport %d accept", s.SetupPort)
	}
	w("\t}")
	w("\tchain forward {")
	w("\t\ttype filter hook forward priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	inside("apps_out")
	w("\t}")
	// Apps to this machine: only the DNS an engine serves on a bridge's gateway.
	w("\tchain apps_in {")
	w("\t\tmeta l4proto { tcp, udp } th dport 53 accept")
	w("\t\tdrop")
	w("\t}")
	// Apps out: their own bridges, and the internet; never the LAN, the mesh or a private range.
	w("\tchain apps_out {")
	notLANOut := ""
	if len(s.LAN) > 0 {
		notLANOut = " oifname != " + set(quoted(s.LAN))
	}
	for _, br := range Bridges {
		w("\t\toifname %q%s accept", br, notLANOut)
	}
	if len(s.Mesh) > 0 {
		w("\t\toifname %s drop", set(quoted(s.Mesh)))
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
