// Package firewall is the agent's firewall table: one nftables table, inet homerslab, that keeps the LAN away from
// everything but the machine itself (spec: the agent owns the firewall table; story 73).
//
// What it does, on every interface that is neither loopback, a mesh nor a container bridge (the LAN, and anything
// else plugged in):
//   - no published ports: nothing that arrives there is forwarded, so Docker's DNAT to a container is dropped too;
//   - listeners on the mesh only: no new connection reaches this machine, except
//   - the machine itself: ICMP (ping, IPv6 neighbour discovery), DHCP and mDNS replies, and the mesh's WireGuard
//     port, so peers at home connect directly;
//   - the setup link's LAN listener, while it is open, until the admin's first sign-in over the mesh closes it;
//   - nothing from the LAN is routed.
//
// A drop in any base chain is final in nftables, whatever other tables (firewalld, Docker's) accept, so this table
// holds whatever else the machine runs. It changes no other table.
package firewall

import (
	"context"
	"errors"
	"fmt"
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

// State is what the table lets in.
type State struct {
	// Mesh are the mesh interfaces (NetBird's wt0). Their own clients filter them by the mesh's policies.
	Mesh []string `json:"mesh"`
	// WireGuard are the mesh clients' UDP ports, open from anywhere so peers connect directly.
	WireGuard []int `json:"wireGuard"`
	// SetupPort is the setup link's LAN listener while it is open, or 0.
	SetupPort int `json:"setupPort"`
}

// Render is the nftables script that replaces the table with s, atomically: one transaction, so no packet ever meets
// a half-made table.
func Render(s State) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("# HomersLab's agent owns this table, and puts it back when it drifts.")
	w("table %s", Table)
	w("delete table %s", Table)
	w("table %s {", Table)
	inside := func() {
		w("\t\tiif \"lo\" accept")
		for _, m := range s.Mesh {
			w("\t\tiifname %q accept", m)
		}
		for _, br := range Bridges {
			w("\t\tiifname %q accept", br)
		}
	}
	w("\tchain input {")
	w("\t\ttype filter hook input priority filter - 5; policy drop;")
	w("\t\tct state established,related accept")
	w("\t\tct state invalid drop")
	inside()
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
	inside()
	w("\t}")
	w("}")
	return b.String()
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
