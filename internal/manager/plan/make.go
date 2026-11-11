package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// creates is what the identity provider gets for each sign-in method.
var creates = map[catalog.SignInMethod]string{catalog.OIDC: "oidc-client", catalog.LDAP: "ldap-access", catalog.Guard: "guard"}

// Make plans installing the app d, from the catalog named from, whose compose project is p, into household h. The
// plan is made at made, which its id leaves out.
func Make(d catalog.App, from string, p engine.Project, h Household, made time.Time) (Plan, error) {
	method, ok := signIn(d.SignIn, h.Provider.Provides)
	if !ok {
		return Plan{}, ErrNoSignInMethod
	}
	services, err := placement(d, p, h.Primary)
	if err != nil {
		return Plan{}, err
	}
	pl := Plan{
		Kind:     "install",
		App:      App{ID: d.ID, Name: d.Name, Purpose: d.Purpose, Version: d.Versions.App, Tier: d.Tier, Catalog: from},
		Name:     Name{Host: d.Names.Name + "." + h.Domain, Meshes: orEmpty(h.Meshes), Exposure: d.Names.Exposure, PublicAllowed: d.Names.Public},
		Audience: Audience{Default: d.Audience.Default, People: audience(d.Audience.Default, h.People), KidView: d.Audience.KidView},
		SignIn:   SignIn{Method: method, Provider: h.Provider, Creates: creates[method]},
		Backups:  backups(d.Data),
		Placement: Placement{Node: h.Primary.Facts.Hostname, Architecture: h.Primary.Facts.Architecture, Engine: h.Primary.Engine,
			Project: p.Name, Services: services},
		Extras:  []Extra{},
		Secrets: []Secret{},
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
	for _, s := range p.Services {
		pin, ok := pins[s.Name]
		if !ok {
			return nil, fmt.Errorf("%w: the compose file runs %s, which the manifest doesn't pin", ErrDefinition, s.Name)
		}
		delete(pins, s.Name)
		out = append(out, Service{Name: s.Name, Image: s.Image, Digest: pin.Digests[n.Engine][catalog.Arch(n.Facts.Architecture)]})
	}
	for name := range pins {
		return nil, fmt.Errorf("%w: the manifest pins %s, which the compose file doesn't run", ErrDefinition, name)
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

// resources checks what the app needs against what the primary has: its memory less what the household's apps
// take, the free space for apps' data, its GPUs, and a build of every image for its engine and architecture.
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
	if out.Memory.FreeBytes < out.Memory.NeedsBytes {
		out.Warnings = append(out.Warnings, Warning{Kind: "memory", NeedsBytes: out.Memory.NeedsBytes, FreeBytes: out.Memory.FreeBytes})
	}
	if f.Space != nil {
		out.Disk.FreeBytes = f.Space.FreeBytes
	}
	if f.Space == nil || out.Disk.FreeBytes < out.Disk.NeedsBytes {
		out.Warnings = append(out.Warnings, Warning{Kind: "disk", NeedsBytes: out.Disk.NeedsBytes, FreeBytes: out.Disk.FreeBytes})
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

// id is the plan's id: a hash of everything it says, except when it was made.
func id(p Plan) string {
	p.ID, p.MadeAt = "", time.Time{}
	b, err := json.Marshal(p)
	if err != nil {
		panic(err) // a plan is plain data
	}
	sum := sha256.Sum256(b)
	return "p-" + hex.EncodeToString(sum[:16])
}
