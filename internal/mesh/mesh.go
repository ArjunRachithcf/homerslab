// Package mesh is the mesh contract: what HomersLab needs from NetBird, Tailscale and Headscale alike (spec: Adapter
// contracts → Meshes; ADR 0005). Each mesh is an adapter in its own package, and every adapter passes the same
// contract suite against the real software in containers.
package mesh

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"time"
)

// Role is what a person may do. Every role is a group on the mesh, and has its own policy.
type Role string

const (
	Admin Role = "admin"
	Adult Role = "adult"
	Kid   Role = "kid"
)

// Roles are every role, in order.
var Roles = []Role{Admin, Adult, Kid}

// Group names on the mesh. They carry HomersLab's prefix, so a household's own groups are never touched.
const (
	NodesGroup = "homerslab-nodes"
)

// RoleGroup is the mesh group of a role's devices.
func RoleGroup(r Role) string { return "homerslab-" + string(r) + "s" }

// Rule is one thing a policy lets through: from a group's devices to another group's, on these TCP ports.
type Rule struct {
	Name  string
	From  string // a group name
	To    string // a group name
	Ports []int  // TCP
}

// Policies are the household's mesh policies, one per role and one for the nodes themselves, for the household's port
// (ADR 0021: 443, or the TLS port NetBird's cloud cluster accepts). Everything else is refused: the mesh's own default
// ("everyone reaches everything") is removed.
//   - Every role's devices reach the nodes' edges: HTTPS at the household's port, and plain HTTP that only redirects
//     to it. Which apps a person may open is the edge's business (audiences), not the mesh's.
//   - Nodes reach each other's edges.
func Policies(port int) []Rule {
	out := make([]Rule, 0, len(Roles)+1)
	for _, r := range Roles {
		ports := []int{80, port}
		if port == 80 || port == 0 {
			ports = []int{80}
		}
		out = append(out, Rule{Name: "homerslab-" + string(r) + "s-to-nodes", From: RoleGroup(r), To: NodesGroup, Ports: ports})
	}
	return append(out, Rule{Name: "homerslab-nodes-to-nodes", From: NodesGroup, To: NodesGroup, Ports: []int{port}})
}

// NameGroups are the groups whose machines resolve the household's names: every role's devices, and the nodes.
func NameGroups() []string {
	out := make([]string, 0, len(Roles)+1)
	for _, r := range Roles {
		out = append(out, RoleGroup(r))
	}
	return append(out, NodesGroup)
}

// Policy is one of the mesh's policies, by its ID and name.
type Policy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Secret is a credential. It never prints, never marshals and never logs; Reveal hands it to the one place that needs
// it.
type Secret string

func (Secret) String() string   { return "[secret]" }
func (Secret) GoString() string { return "[secret]" }

// MarshalJSON refuses: a secret never travels outward (spec: Security defaults).
func (Secret) MarshalJSON() ([]byte, error) { return nil, errors.New("a secret is never marshalled") }

// Reveal is the secret itself.
func (s Secret) Reveal() string { return string(s) }

// Token is HomersLab's own credential on a mesh: what it is, and which of the mesh's users and tokens it is. Only
// Value is secret.
type Token struct {
	Value     Secret    `json:"-"`
	UserID    string    `json:"userId"`
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// MaxTokenLife is how long HomersLab's own mesh token may live: NetBird allows 365 days at most, and HomersLab asks
// for no more whatever a server would accept. RotateBefore is how long before it expires the scheduler replaces it.
const (
	MaxTokenLife = 365 * 24 * time.Hour
	RotateBefore = 30 * 24 * time.Hour
)

// JoinKey is a one-time key that makes a machine a node of the mesh. Its value is secret.
type JoinKey struct {
	Value     Secret
	ID        string
	ExpiresAt time.Time
}

// JoinKeyLife is how long a node's join key works: long enough to install the machine, short enough that a key left
// in a terminal's history is spent or dead. HomersLab sets it itself; servers' own minimums vary.
const JoinKeyLife = 24 * time.Hour

// Report is what a mesh can do, as its adapter finds it.
type Report struct {
	Mesh         string `json:"mesh"`         // netbird, tailscale, headscale
	ControlPlane string `json:"controlPlane"` // cloud or self-hosted
	Version      string `json:"version,omitempty"`
	// PerAppNames is how apps get names: wildcard-records, split-dns or app-nodes.
	PerAppNames string `json:"perAppNames"`
	// PublicPath is the mesh's way in from the internet, or "" where it has none; PublicPathReady says whether it
	// can be used right now (NetBird: a connected proxy cluster).
	PublicPath      string `json:"publicPath"`
	PublicPathReady bool   `json:"publicPathReady"`
	// DomainRequired says whether the household needs a domain of its own with this mesh.
	DomainRequired bool `json:"domainRequired"`
	// PortMappingOff says every node's client runs with router port mapping off, as the nodes' agents find it
	// (drop-in in place, and the running service's environment). The control plane can't know: the manager fills it
	// in from the agents.
	PortMappingOff bool `json:"portMappingOff"`
	// Port is the household's port with this mesh, the one URL every name has (ADR 0021): 443, or the TLS port the
	// public path's cluster accepts.
	Port int `json:"port"`
}

// Mesh is one mesh's API, as HomersLab's own service user.
type Mesh interface {
	// Report says what the mesh can do.
	Report(ctx context.Context) (Report, error)
	// EnsurePolicies makes the groups and Policies for the household's port, and corrects any that drifted. It
	// changes nothing that isn't HomersLab's.
	EnsurePolicies(ctx context.Context, port int) error
	// RemoveDefault deletes the mesh's own "everyone reaches everything" policy, recognised by its shape. Only on a
	// control plane HomersLab set up itself; elsewhere it is one of the Conflicts.
	RemoveDefault(ctx context.Context) error
	// EnsureNames makes the household's domain resolve on the mesh, for the NameGroups' machines only: every name
	// under it (a wildcard) at the node's address to. Records of HomersLab's that drifted are put back.
	EnsureNames(ctx context.Context, domain string, to netip.Addr) error
	// Conflicts are the household's own policies that would let devices reach HomersLab's nodes past the policies per
	// role: enabled accept rules, not HomersLab's, reaching every peer or any group or peer that is a node. Disable
	// switches them off, keeping each exactly as it was otherwise; only with the admin's yes.
	Conflicts(ctx context.Context) ([]Policy, error)
	Disable(ctx context.Context, ids []string) error
	// NewJoinKey makes a one-time key for one node, named so the mesh's dashboard says what it is for.
	NewJoinKey(ctx context.Context, node string) (JoinKey, error)
	// NewDeviceKey makes a one-time key that puts one of a person's devices in their role's group: the admin's first
	// device, in first-time setup's second half (#17). It lasts JoinKeyLife at most.
	NewDeviceKey(ctx context.Context, r Role, device string) (JoinKey, error)
	// RevokeKey ends a join or device key that hasn't been used, by its ID: one that a newer key replaced.
	RevokeKey(ctx context.Context, id string) error
	// Rotate makes HomersLab's next token, which the caller keeps before calling Revoke on the current one.
	Rotate(ctx context.Context) (Token, error)
	// Revoke revokes one of HomersLab's own tokens.
	Revoke(ctx context.Context, t Token) error
	// Token is the token this Mesh uses; With is the same mesh with another of HomersLab's tokens.
	Token() Token
	With(t Token) Mesh
}

// Errors an adapter returns, so callers can say what to fix.
var (
	ErrTokenRefused  = errors.New("the mesh refused the token")
	ErrUnreachable   = errors.New("the mesh's control plane can't be reached")
	ErrAlreadySetUp  = errors.New("the mesh's control plane is set up already: it needs a token")
	ErrNotAMesh      = errors.New("no mesh control plane answers at this address")
	ErrInsecureURL   = errors.New("a control plane is reached over HTTPS only")
	ErrNeedsNewSetup = errors.New("the control plane needs its first setup")
	ErrNamesTaken    = errors.New("the mesh already serves this domain in a zone that isn't HomersLab's")
)

// CheckURL is a control plane's address as HomersLab will use it: HTTPS, or plain HTTP to this machine itself only
// (a test's server). Tokens never cross a network in the clear.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrNotAMesh
	}
	switch u.Scheme {
	case "https":
	case "http":
		a, err := netip.ParseAddr(u.Hostname())
		if err != nil || !a.IsLoopback() {
			return nil, ErrInsecureURL
		}
	default:
		return nil, ErrInsecureURL
	}
	u.Path = ""
	return u, nil
}
