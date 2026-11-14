// Package meshtest is the mesh contract suite: what every mesh adapter must do, checked against the real mesh in
// containers (spec: Testing Decisions). A mesh's own test supplies a World, its control plane and machines, and
// calls Run.
package meshtest

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Machine is one machine in a World: the primary, a second node, or a person's device.
type Machine string

// World is a mesh's control plane with machines around it, as a contract test builds it.
type World interface {
	// Mesh is the adapter, with HomersLab's own token.
	Mesh() mesh.Mesh
	// Join joins a node machine with a key, the way the agent does, and returns its mesh address.
	Join(t *testing.T, m Machine, k mesh.JoinKey) (netip.Addr, error)
	// JoinDevice joins a person's device with the given role, and returns its mesh address.
	JoinDevice(t *testing.T, m Machine, r mesh.Role) netip.Addr
	// Groups are the mesh groups a joined machine is in.
	Groups(t *testing.T, m Machine) []string
	// Policies are the account's policies, by name: what each lets through, from and to which groups.
	Policies(t *testing.T) map[string]mesh.Rule
	// Listen opens a TCP listener on a machine; Reaches says whether a machine connects to an address and port.
	Listen(t *testing.T, m Machine, port int)
	Reaches(t *testing.T, from Machine, to netip.Addr, port int) bool
	// AskedForAPort says whether a machine's client tried to map a port on the router (UPnP, NAT-PMP, PCP).
	AskedForAPort(t *testing.T, m Machine) bool
	// Resolves says whether a machine's own resolver answers a name.
	Resolves(t *testing.T, m Machine, name string) bool
	// Lookup is what a machine's own resolver answers for a name, asked once: its addresses, or none.
	Lookup(t *testing.T, m Machine, name string) []netip.Addr
	// JoinOutsider joins a machine that is in none of HomersLab's groups: a peer the household made by hand.
	JoinOutsider(t *testing.T, m Machine) netip.Addr
	// MeshName is a joined machine's name on the mesh; OutsideName is a name the mesh doesn't serve, from the
	// machine's own DNS.
	MeshName(t *testing.T, m Machine) string
	OutsideName() string
}

// Domain is the household's domain in the suite.
const Domain = "example.test"

// Machines the suite uses.
type Machines struct {
	Primary Machine // a node with the client as the agent configures it
	Second  Machine // a node whose resolver is systemd-resolved
	Device  Machine // a person's device, with the client's own defaults
	// Outsider is a machine on the mesh that isn't HomersLab's: in none of its groups.
	Outsider Machine
}

// Run runs the contract against a World, and returns the adapter with HomersLab's token as the suite left it
// (rotated).
func Run(t *testing.T, w World, ms Machines, want mesh.Report) mesh.Mesh {
	ctx := context.Background()
	m := w.Mesh()

	t.Run("a report of what it can do", func(t *testing.T) {
		r, err := m.Report(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r != want {
			t.Errorf("report:\n got %+v\nwant %+v", r, want)
		}
	})

	t.Run("policies per role, and the mesh's own default removed where HomersLab set the mesh up", func(t *testing.T) {
		if err := m.EnsurePolicies(ctx, want.Port); err != nil {
			t.Fatal(err)
		}
		if err := m.RemoveDefault(ctx); err != nil {
			t.Fatal(err)
		}
		checkPolicies(t, w.Policies(t), want.Port)
		if err := m.EnsurePolicies(ctx, want.Port); err != nil { // again: nothing more, nothing less
			t.Fatal(err)
		}
		checkPolicies(t, w.Policies(t), want.Port)
	})

	var primary netip.Addr
	t.Run("a key for joining a node", func(t *testing.T) {
		k, err := m.NewJoinKey(ctx, "study-pc")
		if err != nil {
			t.Fatal(err)
		}
		if k.ExpiresAt.After(time.Now().Add(mesh.JoinKeyLife + time.Minute)) {
			t.Errorf("the join key expires at %v, beyond %v", k.ExpiresAt, mesh.JoinKeyLife)
		}
		if primary, err = w.Join(t, ms.Primary, k); err != nil {
			t.Fatalf("the primary didn't join with its key: %v", err)
		}
		if !primary.IsValid() {
			t.Fatal("the primary has no mesh address")
		}
		if g := w.Groups(t, ms.Primary); !slices.Contains(g, mesh.NodesGroup) {
			t.Errorf("the primary is in %v, want %s", g, mesh.NodesGroup)
		}
		if _, err := w.Join(t, ms.Second, k); err == nil {
			t.Error("a second machine joined with the same one-time key")
		}
	})
	if !primary.IsValid() {
		t.Fatal("no primary on the mesh: the rest can't be checked")
		return m
	}

	t.Run("a one-time key for a person's device puts it in their role's group (#17)", func(t *testing.T) {
		k, err := m.NewDeviceKey(ctx, mesh.Admin, "first")
		if err != nil {
			t.Fatal(err)
		}
		if k.Value == "" || k.ExpiresAt.After(time.Now().Add(mesh.JoinKeyLife+time.Minute)) {
			t.Errorf("the device key: expires %v", k.ExpiresAt)
		}
		if _, err := m.NewDeviceKey(ctx, mesh.Role("superuser"), "x"); err == nil {
			t.Error("a key for a role that isn't one")
		}
		// Asked again, setup makes a new key and revokes the older one: it joins nothing any more.
		if err := m.RevokeKey(ctx, k.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Join(t, ms.Second, k); err == nil {
			t.Error("a machine joined with a revoked device key")
		}
		if err := m.RevokeKey(ctx, k.ID); err != nil {
			t.Errorf("revoking it again: %v", err)
		}
	})

	t.Run("the policies hold between machines", func(t *testing.T) {
		w.Listen(t, ms.Primary, 443)
		w.Listen(t, ms.Primary, 22)
		w.JoinDevice(t, ms.Device, mesh.Adult)
		if !w.Reaches(t, ms.Device, primary, 443) {
			t.Error("an adult's device can't reach the primary's edge on 443")
		}
		if w.Reaches(t, ms.Device, primary, 22) {
			t.Error("an adult's device reaches the primary on 22, which no policy allows")
		}
	})

	t.Run("the household's names reach the node, for HomersLab's groups only", func(t *testing.T) {
		for range 2 { // again: nothing changes
			if err := m.EnsureNames(ctx, Domain, primary); err != nil {
				t.Fatal(err)
			}
		}
		w.JoinOutsider(t, ms.Outsider)
		// A client takes its zones a moment after they change: the household's machines are asked until they answer.
		eventually := func(mc Machine, name string) []netip.Addr {
			var got []netip.Addr
			for range 30 {
				if got = w.Lookup(t, mc, name); len(got) > 0 {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			return got
		}
		for _, mc := range []Machine{ms.Device, ms.Primary} {
			if got := eventually(mc, "admin."+Domain); !slices.Equal(got, []netip.Addr{primary}) {
				t.Errorf("%s resolves admin.%s to %v, want the primary's %v", mc, Domain, got, primary)
			}
			if got := eventually(mc, "photos."+Domain); !slices.Equal(got, []netip.Addr{primary}) {
				t.Errorf("%s resolves photos.%s to %v: the wildcard should answer every name", mc, Domain, got)
			}
		}
		// The outsider joined before the household's machines were asked: had the zone reached it, it would answer now.
		if got := w.Lookup(t, ms.Outsider, "admin."+Domain); len(got) != 0 {
			t.Errorf("a peer in none of HomersLab's groups resolves admin.%s to %v", Domain, got)
		}
	})

	t.Run("router port mapping off in the node's client", func(t *testing.T) {
		if w.AskedForAPort(t, ms.Primary) {
			t.Error("the primary's client asked the router for a port")
		}
		// The control: the same client with its defaults does ask, so the check above can see it.
		if !w.AskedForAPort(t, ms.Device) {
			t.Error("a client with its defaults didn't try to map a port: the check can't tell")
		}
	})

	t.Run("the machine's resolver keeps working beside the mesh's names", func(t *testing.T) {
		k, err := m.NewJoinKey(ctx, "pi")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Join(t, ms.Second, k); err != nil {
			t.Fatalf("the second node didn't join: %v", err)
		}
		for _, mc := range []Machine{ms.Primary, ms.Second} {
			if !w.Resolves(t, mc, w.OutsideName()) {
				t.Errorf("%s no longer resolves %s, a name outside the mesh", mc, w.OutsideName())
			}
			other := ms.Device
			if name := w.MeshName(t, other); !w.Resolves(t, mc, name) {
				t.Errorf("%s doesn't resolve the mesh's name %s", mc, name)
			}
		}
	})

	t.Run("HomersLab's token rotates by itself", func(t *testing.T) {
		next, err := m.Rotate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next.ExpiresAt.After(time.Now().Add(mesh.MaxTokenLife + time.Hour)) {
			t.Errorf("the new token expires at %v, beyond %v", next.ExpiresAt, mesh.MaxTokenLife)
		}
		if next.Value == "" || next.ID == "" {
			t.Fatalf("a token without a value or ID: %+v", next)
		}
		old := m
		m = m.With(next)
		if _, err := m.Report(ctx); err != nil {
			t.Fatalf("the new token doesn't work: %v", err)
		}
		if err := m.Revoke(ctx, old.Token()); err != nil {
			t.Fatal(err)
		}
		if _, err := old.Report(ctx); !errors.Is(err, mesh.ErrTokenRefused) {
			t.Errorf("the old token after its revocation: %v, want ErrTokenRefused", err)
		}
	})
	return m
}

// checkPolicies wants HomersLab's policies for the household's port exactly, none other of its name, and none of the
// mesh's own default.
func checkPolicies(t *testing.T, have map[string]mesh.Rule, port int) {
	t.Helper()
	if _, ok := have["Default"]; ok {
		t.Error("the mesh's Default policy is still there")
	}
	want := map[string]bool{}
	for _, w := range mesh.Policies(port) {
		want[w.Name] = true
		got, ok := have[w.Name]
		if !ok {
			t.Errorf("no policy %s", w.Name)
			continue
		}
		slices.Sort(got.Ports)
		if got.From != w.From || got.To != w.To || !slices.Equal(got.Ports, w.Ports) {
			t.Errorf("policy %s: %+v, want %+v", w.Name, got, w)
		}
	}
	for name := range have {
		if strings.HasPrefix(name, "homerslab-") && !want[name] {
			t.Errorf("a policy %s that HomersLab doesn't make", name)
		}
	}
}
