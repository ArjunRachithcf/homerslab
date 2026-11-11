// Package plan is the install planner: it turns an app definition, the compose project it runs and the household's
// state into one install plan (spec: The install plan). A plan says what installing will do, in facts that Admin, the
// CLI and the MCP server put in plain words the same way; nothing happens until it is applied. A plan is immutable,
// and its id is a hash of what it says: a change in the household, or in the definition, is a new plan.
package plan

import (
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// Plan is one install plan.
type Plan struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"` // install
	MadeAt time.Time `json:"madeAt"`

	App       App       `json:"app"`
	Name      Name      `json:"name"`
	Audience  Audience  `json:"audience"`
	SignIn    SignIn    `json:"signIn"`
	Backups   Backups   `json:"backups"`
	Placement Placement `json:"placement"`
	Extras    []Extra   `json:"extras"`
	Secrets   []Secret  `json:"secrets"`
	Resources Resources `json:"resources"`
	// Risky says applying needs the admin's passkey: the plan gives the app an extra.
	Risky bool `json:"risky"`
}

// App is the app the plan installs, at which version, from which catalog and publisher: an app's id is its catalog's.
type App struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Purpose   string       `json:"purpose"`
	Version   string       `json:"version"`
	Tier      catalog.Tier `json:"tier"`
	Catalog   string       `json:"catalog"`
	Publisher string       `json:"publisher"`
	// Definition is the digest of what the app's definition is made of (catalog.App.Digest): the plan is made from it.
	Definition string `json:"definition"`
}

// Name is the app's name on the household's main domain, and who can reach it. When the name the app asked for is
// reserved or taken, Asked is that name and Why says by what: platform, mesh, dns, web, mail, node or app.
type Name struct {
	Host     string   `json:"host"`
	Asked    string   `json:"asked,omitempty"`
	Why      string   `json:"why,omitempty"`
	Meshes   []string `json:"meshes"`
	Exposure string   `json:"exposure"` // mesh
	// PublicAllowed says the app may be made public later.
	PublicAllowed bool `json:"publicAllowed"`
}

// Audience is who will see the app: its default audience, the people in it today, and the kid view.
type Audience struct {
	Default string   `json:"default"` // everyone, adults or admins
	People  []Person `json:"people"`
	KidView string   `json:"kidView,omitempty"`
}

// SignIn is how people will sign in: the first method the app lists that the household's identity provider provides,
// and what installing creates for it.
type SignIn struct {
	Method   catalog.SignInMethod `json:"method"`
	Provider Provider             `json:"provider"`
	// Creates is what the identity provider gets: an OIDC client, LDAP access, or a guard at the edge.
	Creates string `json:"creates"`
}

// Backups is what is backed up, and when.
type Backups struct {
	Every       string        `json:"every"`       // hour
	RestoreTest string        `json:"restoreTest"` // week
	Data        []BackedUp    `json:"data"`
	Left        []NotBackedUp `json:"left"`
}

// BackedUp is one thing of the app's that is backed up: a folder, or a database dump.
type BackedUp struct {
	What string `json:"what"`
	Kind string `json:"kind"` // folder or dump
}

// NotBackedUp is persistent data the backups leave out, and why.
type NotBackedUp struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// Placement is where the app runs: its node, its engine, and the compose project's services.
type Placement struct {
	Node         string         `json:"node"`
	Architecture string         `json:"architecture"`
	Engine       catalog.Engine `json:"engine"`
	Project      string         `json:"project"`
	Services     []Service      `json:"services"`
}

// Service is one container the app runs, with its image and the digest this node pulls.
type Service struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Digest string `json:"digest,omitempty"` // empty when there is no build for this node
}

// Extra is a permission beyond the floor that the plan gives the app.
type Extra struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Service    string `json:"service"`
	Device     string `json:"device,omitempty"`
	Capability string `json:"capability,omitempty"`
	Why        string `json:"why"`
	Optional   bool   `json:"optional"`
}

// Secret is one secret installing makes on the node, sealed to its TPM where it has one.
type Secret struct {
	Name     string `json:"name"`
	What     string `json:"what"`
	SealedTo string `json:"sealedTo"` // tpm, or host when the node has no TPM
}

// Resources is the check of what the app needs against what its node has. Warnings say what is short.
type Resources struct {
	Memory   Amount    `json:"memory"`
	Disk     Amount    `json:"disk"`
	GPU      GPU       `json:"gpu"`
	Warnings []Warning `json:"warnings"`
}

// Amount is what the app needs and what the node has free, in bytes: for memory, what is available now.
type Amount struct {
	NeedsBytes uint64 `json:"needsBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	Unknown    bool   `json:"unknown,omitempty"` // the node can't say what is free
	Grows      string `json:"grows,omitempty"`
}

// GPU is whether the app wants a GPU, and which of the node's it can use.
type GPU struct {
	Need    string   `json:"need"` // none, optional or required
	For     string   `json:"for,omitempty"`
	Vendors []string `json:"vendors,omitempty"`
	Usable  []string `json:"usable"` // the node's GPUs it can use: "NVIDIA GeForce RTX 3060"
}

// Warning is one thing the node lacks: memory, disk, a GPU, or a build of the app for its architecture and engine.
type Warning struct {
	Kind       string   `json:"kind"` // memory, disk, gpu or build
	NeedsBytes uint64   `json:"needsBytes,omitempty"`
	FreeBytes  uint64   `json:"freeBytes,omitempty"`
	Services   []string `json:"services,omitempty"` // build: the services with no image for this node
	Unknown    bool     `json:"unknown,omitempty"`  // disk: the node can't say what is free
}

// Household is the household's state, as the planner needs it.
type Household struct {
	Name     string
	Domain   string   // the main domain
	Meshes   []string // NetBird, Tailscale, Headscale
	Provider Provider
	People   []Person
	Primary  Node
	// Installed are the apps already in the household, with what they take of the primary.
	Installed []Installed
}

// Provider is the household's identity provider and the sign-in methods it reports it provides.
type Provider struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Provides []catalog.SignInMethod `json:"-"`
}

// Person is someone in the household and their role: admin, adult or kid.
type Person struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Node is a node as its agent reports it, and the container engine it runs.
type Node struct {
	Facts  node.Facts
	Engine catalog.Engine
}

// Installed is an app already in the household: its name on the domains, and what it takes.
type Installed struct {
	App       string
	Name      string
	MemoryMiB int
}
