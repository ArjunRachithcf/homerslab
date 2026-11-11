package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

// The ways a definition can't be planned.
var (
	// ErrNoSignInMethod: none of the app's sign-in methods is one the household's identity provider provides.
	ErrNoSignInMethod = errors.New("no-sign-in-method")
	// ErrDefinition: the definition's manifest and its compose file disagree.
	ErrDefinition = errors.New("definition-mismatch")
)

// The schedule every app's backups follow (ADR 0010).
const (
	backupEvery = "hour"
	restoreTest = "week"
)

// pinned is an image named by its digest.
var pinned = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// creates is what the identity provider gets for each sign-in method.
var creates = map[catalog.SignInMethod]string{catalog.OIDC: "oidc-client", catalog.LDAP: "ldap-access", catalog.Guard: "guard"}

// From is the catalog an app comes from.
type From struct{ ID, Publisher string }

// Make plans installing the app d, from catalog from, whose compose project is p, into household h. The plan is
// made at made, which its id leaves out.
func Make(d catalog.App, from From, p engine.Project, h Household, made time.Time) (Plan, error) {
	method, ok := signIn(d.SignIn, h.Provider.Provides)
	if !ok {
		return Plan{}, ErrNoSignInMethod
	}
	services, err := placement(d, p, h.Primary)
	if err != nil {
		return Plan{}, err
	}
	host, why := name(d.Names.Name, h)
	pl := Plan{
		Kind:     "install",
		App:      App{ID: d.ID, Name: d.Name, Purpose: d.Purpose, Version: d.Versions.App, Tier: d.Tier, Catalog: from.ID, Publisher: from.Publisher, Definition: d.Digest},
		Name:     Name{Host: host + "." + h.Domain, Meshes: orEmpty(h.Meshes), Exposure: d.Names.Exposure, PublicAllowed: d.Names.Public},
		Audience: Audience{Default: d.Audience.Default, People: audience(d.Audience.Default, h.People), KidView: d.Audience.KidView},
		SignIn:   SignIn{Method: method, Provider: h.Provider, Creates: creates[method]},
		Backups:  backups(d.Data),
		Placement: Placement{Node: h.Primary.Facts.Hostname, Architecture: h.Primary.Facts.Architecture, Engine: h.Primary.Engine,
			Project: p.Name, Services: services},
		Extras:  []Extra{},
		Secrets: []Secret{},
	}
	if why != "" {
		pl.Name.Asked, pl.Name.Why = d.Names.Name, why
	}
	for _, x := range d.Extras {
		pl.Extras = append(pl.Extras, Extra{ID: x.ID, Kind: x.Kind, Service: x.Service, Device: x.Device, Capability: x.Capability, Why: x.Why, Optional: x.Optional})
	}
	sealed := "host"
	if h.Primary.Facts.TPM.Present {
		sealed = "tpm"
	}
	for _, s := range d.Secrets {
		pl.Secrets = append(pl.Secrets, Secret{Name: s.Name, What: s.What, SealedTo: sealed})
	}
	pl.Resources = resources(d.Resources, h, services)
	pl.Risky = len(pl.Extras) > 0
	pl.MadeAt = made.UTC()
	pl.ID = id(pl)
	return pl, nil
}

// name is the app's name: the one it asks for, or, when that is reserved or taken, the next free one (photos-2), and
// what it was reserved or taken by.
func name(want string, h Household) (string, string) {
	why := taken(want, h)
	if why == "" {
		return want, ""
	}
	base := strings.TrimLeft(want, "_") // DNS's own names start with one; the app's never does
	if base == "" {
		base = "app"
	}
	if base != want && taken(base, h) == "" {
		return base, why
	}
	for n := 2; ; n++ {
		if next := fmt.Sprintf("%s-%d", base, n); taken(next, h) == "" {
			return next, why
		}
	}
}

// taken says what already uses a name: the platform, the mesh, DNS, the web or mail (catalog.Reserved), a node, or
// another app; "" when it is free.
func taken(name string, h Household) string {
	if why := catalog.Reserved(name); why != "" {
		return why
	}
	if strings.EqualFold(name, h.Primary.Facts.Hostname) {
		return "node"
	}
	for _, a := range h.Installed {
		if strings.EqualFold(name, a.Name) {
			return "app"
		}
	}
	return ""
}

// signIn is the first of the app's methods, in its order, that the provider provides.
func signIn(methods []catalog.SignIn, provides []catalog.SignInMethod) (catalog.SignInMethod, bool) {
	for _, m := range methods {
		if slices.Contains(provides, m.Method) {
			return m.Method, true
		}
	}
	return "", false
}

// placement pairs every service the compose project runs with its pin in the manifest, and the digest this node's
// engine pulls on its architecture.
func placement(d catalog.App, p engine.Project, n Node) ([]Service, error) {
	pins := map[string]catalog.Image{}
	for _, im := range d.Versions.Images {
		pins[im.Service] = im
	}
	out := []Service{}
	runs := map[string]bool{}
	for _, s := range p.Services {
		runs[s.Name] = true
		pin, ok := pins[s.Name]
		if !ok {
			return nil, fmt.Errorf("%w: the compose file runs %s, which the manifest doesn't pin", ErrDefinition, s.Name)
		}
		if !pinned.MatchString(s.Image) {
			return nil, fmt.Errorf("%w: %s's image %s isn't pinned by digest", ErrDefinition, s.Name, s.Image)
		}
		delete(pins, s.Name)
		out = append(out, Service{Name: s.Name, Image: s.Image, Digest: pin.Digests[n.Engine][catalog.Arch(n.Facts.Architecture)]})
	}
	for name := range pins {
		return nil, fmt.Errorf("%w: the manifest pins %s, which the compose file doesn't run", ErrDefinition, name)
	}
	named := []string{d.Names.Service}
	for _, x := range d.Data {
		named = append(named, x.Service)
	}
	for _, x := range d.Health {
		named = append(named, x.Service)
	}
	for _, x := range d.Extras {
		named = append(named, x.Service)
	}
	for _, name := range named {
		if !runs[name] {
			return nil, fmt.Errorf("%w: the manifest names service %s, which the compose file doesn't run", ErrDefinition, name)
		}
	}
	return out, nil
}

// audience is the household's people in an app's default audience.
func audience(def string, people []Person) []Person {
	out := []Person{}
	for _, p := range people {
		if def == "everyone" || (def == "adults" && p.Role != "kid") || (def == "admins" && p.Role == "admin") {
			out = append(out, p)
		}
	}
	return out
}

func backups(data []catalog.Data) Backups {
	b := Backups{Every: backupEvery, RestoreTest: restoreTest, Data: []BackedUp{}, Left: []NotBackedUp{}}
	for _, d := range data {
		switch {
		case !d.BackedUp():
			b.Left = append(b.Left, NotBackedUp{What: d.What, Why: d.Why})
		case d.Dump != nil:
			b.Data = append(b.Data, BackedUp{What: d.What, Kind: "dump"})
		default:
			b.Data = append(b.Data, BackedUp{What: d.What, Kind: "folder"})
		}
	}
	return b
}

// resources checks what the app needs against what the primary has: the memory available now, and never more than
// its memory less what the household's apps take, the free space for apps' data, its GPUs, and a build of every image for its engine and architecture.
func resources(r catalog.Resources, h Household, services []Service) Resources {
	f := h.Primary.Facts
	out := Resources{
		Memory:   Amount{NeedsBytes: mib(r.MemoryMiB), FreeBytes: f.MemoryBytes},
		Disk:     Amount{NeedsBytes: mib(r.DiskGiB * 1024), Grows: r.Grows},
		GPU:      GPU{Need: r.GPU.Need, For: r.GPU.For, Vendors: r.GPU.Vendors, Usable: []string{}},
		Warnings: []Warning{},
	}
	for _, a := range h.Installed {
		out.Memory.FreeBytes -= min(out.Memory.FreeBytes, mib(a.MemoryMiB))
	}
	// What the kernel says is available now, when it says: what the memory the apps take leaves is the most there is.
	if f.MemoryAvailableBytes > 0 {
		out.Memory.FreeBytes = min(out.Memory.FreeBytes, f.MemoryAvailableBytes)
	}
	if out.Memory.FreeBytes < out.Memory.NeedsBytes {
		out.Warnings = append(out.Warnings, Warning{Kind: "memory", NeedsBytes: out.Memory.NeedsBytes, FreeBytes: out.Memory.FreeBytes})
	}
	if f.Space != nil {
		out.Disk.FreeBytes = f.Space.FreeBytes
	}
	out.Disk.Unknown = f.Space == nil
	if f.Space == nil || out.Disk.FreeBytes < out.Disk.NeedsBytes {
		out.Warnings = append(out.Warnings, Warning{Kind: "disk", NeedsBytes: out.Disk.NeedsBytes, FreeBytes: out.Disk.FreeBytes, Unknown: out.Disk.Unknown})
	}
	if r.GPU.Need != "none" {
		for _, g := range f.GPUs {
			if slices.Contains(r.GPU.Vendors, strings.ToLower(g.Vendor)) {
				out.GPU.Usable = append(out.GPU.Usable, strings.TrimSpace(g.Vendor+" "+g.Model))
			}
		}
	}
	if r.GPU.Need == "required" && len(out.GPU.Usable) == 0 {
		out.Warnings = append(out.Warnings, Warning{Kind: "gpu"})
	}
	var unbuilt []string
	for _, s := range services {
		if s.Digest == "" {
			unbuilt = append(unbuilt, s.Name)
		}
	}
	if unbuilt != nil {
		out.Warnings = append(out.Warnings, Warning{Kind: "build", Services: unbuilt})
	}
	return out
}

func mib(n int) uint64 { return uint64(max(n, 0)) << 20 }

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// id is the plan's id: a hash of everything it says, except when it was made and how much the node had free then,
// which moves all the time. Whether the app fits stays in: a warning that comes or goes makes a new plan.
func id(p Plan) string {
	p.ID, p.MadeAt = "", time.Time{}
	p.Resources.Memory.FreeBytes, p.Resources.Disk.FreeBytes = 0, 0
	p.Resources.Warnings = slices.Clone(p.Resources.Warnings)
	for i := range p.Resources.Warnings {
		p.Resources.Warnings[i].FreeBytes = 0
	}
	b, err := json.Marshal(p)
	if err != nil {
		panic(err) // a plan is plain data
	}
	sum := sha256.Sum256(b)
	return "p-" + hex.EncodeToString(sum[:16])
}
