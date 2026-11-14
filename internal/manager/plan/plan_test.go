package plan_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/node"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const gib = 1 << 30

var (
	digestA  = "sha256:" + strings.Repeat("a", 64)
	digestB  = "sha256:" + strings.Repeat("b", 64)
	made     = time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	official = plan.From{ID: "official", Publisher: "HomersLab"}
)

// definition is a small app in two services, with what the planner reads of it.
func definition() catalog.App {
	no := false
	return catalog.App{
		ID: "pictures", Name: "Pictures", Purpose: "Photos", Category: "photos", Tier: catalog.Verified, Digest: "sha256:" + strings.Repeat("d", 64),
		Versions: catalog.Versions{App: "v2.0.1", Images: []catalog.Image{
			{Service: "web", Digests: map[catalog.Engine]map[catalog.Arch]string{
				catalog.Docker: {catalog.X86_64: digestA, catalog.ARM64: digestB},
				catalog.Podman: {catalog.X86_64: digestA},
			}},
			{Service: "db", Digests: map[catalog.Engine]map[catalog.Arch]string{
				catalog.Docker: {catalog.X86_64: digestB, catalog.ARM64: digestA},
				catalog.Podman: {catalog.X86_64: digestB, catalog.ARM64: digestA},
			}},
		}},
		SignIn:   []catalog.SignIn{{Method: catalog.OIDC}, {Method: catalog.Guard}},
		Names:    catalog.Names{Name: "pictures", Service: "web", Port: 8080, Exposure: "mesh", Public: true},
		Audience: catalog.Audience{Default: "adults"},
		Data: []catalog.Data{
			{What: "The library", Service: "web", Path: "/data"},
			{What: "Its database", Service: "db", Dump: &catalog.Dump{Command: []string{"pg_dump"}, File: "db.sql"}},
			{What: "Thumbnails", Service: "web", Path: "/cache", Backup: &no, Why: "Made again from the library"},
		},
		Secrets:   []catalog.Secret{{Name: "db-password", What: "the database password", UID: 999}},
		Health:    []catalog.Health{{Service: "web", Compose: true}},
		Resources: catalog.Resources{MemoryMiB: 2048, DiskGiB: 10, Grows: "the library", GPU: catalog.GPU{Need: "none"}},
	}
}

func project() engine.Project {
	return engine.Project{Name: "homerslab-pictures", Services: []engine.Service{
		{Name: "db", Image: "postgres:16@" + digestB},
		{Name: "web", Image: "example/pictures:v2.0.1@" + digestA},
	}}
}

func authentik() plan.Provider {
	return plan.Provider{ID: "authentik", Name: "Authentik", Provides: []catalog.SignInMethod{catalog.OIDC, catalog.LDAP, catalog.Guard}}
}

// household is the Ferreiras' household: an admin, an adult and a kid, on a primary with 32 GB of memory and an
// NVIDIA GPU, and 400 GB free.
func household() plan.Household {
	return plan.Household{
		Name: "The Ferreira household", Domain: "ferreira.family", Meshes: []string{"NetBird"}, Provider: authentik(),
		People: []plan.Person{{Name: "Inês Ferreira", Role: "admin"}, {Name: "Rui Ferreira", Role: "adult"}, {Name: "Tomás Ferreira", Role: "kid"}},
		Primary: plan.Node{Engine: catalog.Docker, Facts: node.Facts{
			Hostname: "study-pc", Architecture: "x86_64", MemoryBytes: 32 * gib, MemoryAvailableBytes: 20 * gib, TPM: node.TPM{Present: true, Version: "2.0"},
			GPUs:  []node.GPU{{Vendor: "NVIDIA", Model: "GeForce RTX 3060", PCIID: "10de:2504"}},
			Space: &node.Space{Path: "/var/lib/homerslab", FreeBytes: 400e9, SizeBytes: 1e12},
		}},
	}
}

func make_(t *testing.T, d catalog.App, h plan.Household) plan.Plan {
	t.Helper()
	p, err := plan.Make(d, official, project(), h, made)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A plan holds what installing creates, where it runs, what is backed up and when, its extras and the resources check
// (spec: The install plan).
func TestAPlanSaysWhatInstallingDoes(t *testing.T) {
	testenv.Isolate(t)
	d := definition()
	d.Extras = []catalog.Extra{{ID: "render", Kind: "device", Service: "web", Device: "/dev/dri/renderD128", Why: "Smaller videos", Optional: true}}
	p := make_(t, d, household())

	if p.Kind != "install" || !strings.HasPrefix(p.ID, "p-") || !p.MadeAt.Equal(made) {
		t.Errorf("plan %s, kind %s, made %s", p.ID, p.Kind, p.MadeAt)
	}
	check := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", what, got, want)
		}
	}
	check("app", p.App, plan.App{ID: "pictures", Name: "Pictures", Purpose: "Photos", Version: "v2.0.1", Tier: catalog.Verified, Catalog: "official", Publisher: "HomersLab", Definition: "sha256:" + strings.Repeat("d", 64)})
	check("name", p.Name, plan.Name{Host: "pictures.ferreira.family", Meshes: []string{"NetBird"}, Exposure: "mesh", PublicAllowed: true})
	check("audience", p.Audience, plan.Audience{Default: "adults", People: []plan.Person{{Name: "Inês Ferreira", Role: "admin"}, {Name: "Rui Ferreira", Role: "adult"}}})
	check("sign-in", p.SignIn, plan.SignIn{Method: catalog.OIDC, Provider: authentik(), Creates: "oidc-client"}) // no env: not wired
	check("backups", p.Backups, plan.Backups{Every: "hour", RestoreTest: "week",
		Data: []plan.BackedUp{{What: "The library", Kind: "folder"}, {What: "Its database", Kind: "dump"}},
		Left: []plan.NotBackedUp{{What: "Thumbnails", Why: "Made again from the library"}}})
	check("placement", p.Placement, plan.Placement{Node: "study-pc", Architecture: "x86_64", Engine: catalog.Docker, Project: "homerslab-pictures",
		Services: []plan.Service{{Name: "db", Image: "postgres:16@" + digestB, Digest: digestB}, {Name: "web", Image: "example/pictures:v2.0.1@" + digestA, Digest: digestA}}})
	check("extras", p.Extras, []plan.Extra{{ID: "render", Kind: "device", Service: "web", Device: "/dev/dri/renderD128", Why: "Smaller videos", Optional: true}})
	check("secrets", p.Secrets, []plan.Secret{{Name: "db-password", What: "the database password", SealedTo: "tpm"}})
	check("resources", p.Resources, plan.Resources{
		Memory: plan.Amount{NeedsBytes: 2 * gib, FreeBytes: 20 * gib}, Disk: plan.Amount{NeedsBytes: 10 * gib, FreeBytes: 400e9, Grows: "the library"},
		GPU: plan.GPU{Need: "none", Usable: []string{}}, Warnings: []plan.Warning{}})
	if !p.Risky {
		t.Error("a plan with an extra needs the passkey")
	}

	h := household()
	h.Primary.Facts.TPM = node.TPM{}
	if s := make_(t, definition(), h).Secrets[0].SealedTo; s != "host" {
		t.Errorf("without a TPM, secrets are sealed to %q, want host", s)
	}
	if make_(t, definition(), household()).Risky {
		t.Error("a plan with no extra needs no passkey")
	}
}

// The plan uses the first sign-in method, in the app's order of preference, that the identity provider provides
// (spec: Adapter contracts, the sign-in method).
func TestThePlanUsesTheFirstSignInMethodTheProviderProvides(t *testing.T) {
	testenv.Isolate(t)
	all := []catalog.SignInMethod{catalog.OIDC, catalog.LDAP, catalog.Guard}
	for _, c := range []struct {
		app      []catalog.SignInMethod
		provides []catalog.SignInMethod
		want     catalog.SignInMethod
		creates  string
	}{
		{all, all, catalog.OIDC, "oidc-client"},
		{all, []catalog.SignInMethod{catalog.Guard, catalog.LDAP}, catalog.LDAP, "ldap-access"},
		{all, []catalog.SignInMethod{catalog.Guard}, catalog.Guard, "guard"},
		{[]catalog.SignInMethod{catalog.LDAP, catalog.OIDC}, all, catalog.LDAP, "ldap-access"},
		{[]catalog.SignInMethod{catalog.Guard}, all, catalog.Guard, "guard"},
	} {
		d, h := definition(), household()
		d.SignIn = nil
		for _, m := range c.app {
			d.SignIn = append(d.SignIn, catalog.SignIn{Method: m})
		}
		h.Provider.Provides = c.provides
		if got := make_(t, d, h).SignIn; got.Method != c.want || got.Creates != c.creates {
			t.Errorf("the app lists %v, the provider provides %v: %s (%s), want %s (%s)", c.app, c.provides, got.Method, got.Creates, c.want, c.creates)
		}
	}

	d, h := definition(), household()
	d.SignIn = []catalog.SignIn{{Method: catalog.OIDC}}
	h.Provider.Provides = []catalog.SignInMethod{catalog.Guard}
	if _, err := plan.Make(d, official, project(), h, made); !errors.Is(err, plan.ErrNoSignInMethod) {
		t.Errorf("no method in common: %v, want ErrNoSignInMethod", err)
	}
}

// The plan names the OIDC client installing creates: its redirect addresses are exactly the app's callbacks on its one
// canonical URL, the household's port included (ADR 0021, #92), or its own apps' addresses, and follow the name the
// app actually gets (#20, adversarial review round 2). It says whether HomersLab can set the method up today (wired).
func TestAnOIDCPlanNamesItsClientsRedirectAddressesOnTheAppsOwnURLAndWhetherItIsWired(t *testing.T) {
	testenv.Isolate(t)
	d, h := definition(), household()
	uid := 911
	takes := &catalog.OIDCEnv{ClientID: "ID", ClientSecretFile: "SECRET_FILE", Issuer: "ISSUER"}
	d.SignIn = []catalog.SignIn{{Method: catalog.OIDC, RedirectURIs: []string{"/auth/callback", "/settings", "app.pictures:///callback"}, Env: takes, SecretUID: &uid},
		{Method: catalog.Guard}}
	h.Port = 9443
	got := make_(t, d, h)
	want := []string{"https://pictures.ferreira.family:9443/auth/callback", "https://pictures.ferreira.family:9443/settings", "app.pictures:///callback"}
	if got.SignIn.Method != catalog.OIDC || !got.SignIn.Wired || !reflect.DeepEqual(got.SignIn.RedirectURIs, want) {
		t.Errorf("on port 9443: %+v, want OIDC, wired, redirect addresses %v", got.SignIn, want)
	}
	h.Port = 443
	if p := make_(t, d, h); p.SignIn.RedirectURIs[0] != "https://pictures.ferreira.family/auth/callback" || p.ID == got.ID {
		t.Errorf("on 443: %v (plan %s, on 9443 %s): the port is part of the URL, and of the plan", p.SignIn.RedirectURIs, p.ID, got.ID)
	}
	h.Installed = []plan.Installed{{App: "other", Name: "pictures"}}
	if p := make_(t, d, h); p.SignIn.RedirectURIs[0] != "https://pictures-2.ferreira.family/auth/callback" {
		t.Errorf("when the name is taken: %v", p.SignIn.RedirectURIs)
	}

	d.SignIn[0].Env = nil // the manifest doesn't say how the app takes its client: still the app's first choice
	if p := make_(t, d, h); p.SignIn.Method != catalog.OIDC || p.SignIn.Wired {
		t.Errorf("OIDC that HomersLab can't set up yet: %+v, want it shown, not wired", p.SignIn)
	}
	d.SignIn = []catalog.SignIn{{Method: catalog.LDAP}}
	if p := make_(t, d, h); p.SignIn.Method != catalog.LDAP || p.SignIn.Wired {
		t.Errorf("an LDAP plan: %+v, want not wired (#49)", p.SignIn)
	}
	d.SignIn = []catalog.SignIn{{Method: catalog.Guard}}
	if p := make_(t, d, h); p.SignIn.RedirectURIs != nil || !p.SignIn.Wired {
		t.Errorf("a guarded plan: %+v, want no redirect addresses, wired", p.SignIn)
	}
}

// The resources check warns when the primary lacks the memory, disk or GPU the app asks for, or a build of it for
// its architecture and engine (story 18).
func TestTheResourcesCheckWarnsWhenThePrimaryLacks(t *testing.T) {
	testenv.Isolate(t)
	for name, c := range map[string]struct {
		edit   func(*catalog.App, *plan.Household)
		want   []plan.Warning
		usable []string
	}{
		"enough of everything": {func(*catalog.App, *plan.Household) {}, []plan.Warning{}, []string{}},
		"not enough memory": {func(d *catalog.App, h *plan.Household) { h.Primary.Facts.MemoryBytes = 1 * gib },
			[]plan.Warning{{Kind: "memory", NeedsBytes: 2 * gib, FreeBytes: 1 * gib}}, []string{}},
		"memory in use": {func(d *catalog.App, h *plan.Household) { h.Primary.Facts.MemoryAvailableBytes = 1 * gib },
			[]plan.Warning{{Kind: "memory", NeedsBytes: 2 * gib, FreeBytes: 1 * gib}}, []string{}},
		"memory the household's apps already take": {func(d *catalog.App, h *plan.Household) {
			h.Installed = []plan.Installed{{App: "films", MemoryMiB: 31 * 1024}}
		}, []plan.Warning{{Kind: "memory", NeedsBytes: 2 * gib, FreeBytes: 1 * gib}}, []string{}},
		"not enough disk": {func(d *catalog.App, h *plan.Household) { h.Primary.Facts.Space.FreeBytes = 5 * gib },
			[]plan.Warning{{Kind: "disk", NeedsBytes: 10 * gib, FreeBytes: 5 * gib}}, []string{}},
		"a disk that can't say": {func(d *catalog.App, h *plan.Household) { h.Primary.Facts.Space = nil },
			[]plan.Warning{{Kind: "disk", NeedsBytes: 10 * gib, Unknown: true}}, []string{}},
		"a GPU it requires, and none": {func(d *catalog.App, h *plan.Household) {
			d.Resources.GPU = catalog.GPU{Need: "required", Vendors: []string{"nvidia"}, For: "transcoding"}
			h.Primary.Facts.GPUs = []node.GPU{}
		}, []plan.Warning{{Kind: "gpu"}}, []string{}},
		"a GPU it requires, of another maker": {func(d *catalog.App, h *plan.Household) {
			d.Resources.GPU = catalog.GPU{Need: "required", Vendors: []string{"intel", "amd"}, For: "transcoding"}
		}, []plan.Warning{{Kind: "gpu"}}, []string{}},
		"a GPU it requires, and there": {func(d *catalog.App, h *plan.Household) {
			d.Resources.GPU = catalog.GPU{Need: "required", Vendors: []string{"nvidia"}, For: "transcoding"}
		}, []plan.Warning{}, []string{"NVIDIA GeForce RTX 3060"}},
		"a GPU it can do without": {func(d *catalog.App, h *plan.Household) {
			d.Resources.GPU = catalog.GPU{Need: "optional", Vendors: []string{"intel"}, For: "faces"}
		}, []plan.Warning{}, []string{}},
		"no build for the architecture": {func(d *catalog.App, h *plan.Household) {
			h.Primary.Engine = catalog.Podman
			h.Primary.Facts.Architecture = "arm64"
		},
			[]plan.Warning{{Kind: "build", Services: []string{"web"}}}, []string{}},
		"everything short at once": {func(d *catalog.App, h *plan.Household) {
			d.Resources.GPU = catalog.GPU{Need: "required", Vendors: []string{"amd"}, For: "transcoding"}
			h.Primary.Facts.MemoryBytes, h.Primary.Facts.Space.FreeBytes = gib, gib
		}, []plan.Warning{{Kind: "memory", NeedsBytes: 2 * gib, FreeBytes: gib}, {Kind: "disk", NeedsBytes: 10 * gib, FreeBytes: gib}, {Kind: "gpu"}}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			d, h := definition(), household()
			c.edit(&d, &h)
			r := make_(t, d, h).Resources
			if !reflect.DeepEqual(r.Warnings, c.want) || !reflect.DeepEqual(r.GPU.Usable, c.usable) {
				t.Errorf("warnings %+v, usable %v\nwant %+v, usable %v", r.Warnings, r.GPU.Usable, c.want, c.usable)
			}
		})
	}
}

// A plan is known by its id, a hash of what it says: the same household and definition give the same plan, and any
// change gives a new one. When it was made isn't part of it.
func TestAPlanIsKnownByWhatItSays(t *testing.T) {
	testenv.Isolate(t)
	first := make_(t, definition(), household())
	again, err := plan.Make(definition(), official, project(), household(), made.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Errorf("the same plan an hour later is %s, want %s", again.ID, first.ID)
	}
	// What the node has free is measured when the plan is made; it moves all the time, so it isn't what makes a plan
	// new. Whether the app fits is: a warning changes the plan.
	h := household()
	h.Primary.Facts.Space.FreeBytes -= 3e6
	if p := make_(t, definition(), h); p.ID != first.ID {
		t.Errorf("a few megabytes less free disk made a new plan, %s", p.ID)
	}
	for name, edit := range map[string]func(*catalog.App, *plan.Household){
		"a new person": func(_ *catalog.App, h *plan.Household) {
			h.People = append(h.People, plan.Person{Name: "Ana", Role: "adult"})
		},
		"another domain": func(_ *catalog.App, h *plan.Household) { h.Domain = "ferreira.casa" },
		"another provider": func(_ *catalog.App, h *plan.Household) {
			h.Provider = plan.Provider{ID: "kanidm", Name: "Kanidm", Provides: h.Provider.Provides}
		},
		"less memory":    func(_ *catalog.App, h *plan.Household) { h.Primary.Facts.MemoryBytes = gib },
		"another engine": func(_ *catalog.App, h *plan.Household) { h.Primary.Engine = catalog.Podman },
		"an app that leaves too little memory": func(_ *catalog.App, h *plan.Household) {
			h.Installed = []plan.Installed{{App: "films", MemoryMiB: 31 * 1024}}
		},
		"a newer version":      func(d *catalog.App, _ *plan.Household) { d.Versions.App = "v2.0.2" },
		"another definition":   func(d *catalog.App, _ *plan.Household) { d.Digest = "sha256:" + strings.Repeat("e", 64) },
		"another default view": func(d *catalog.App, _ *plan.Household) { d.Audience.Default = "everyone" },
	} {
		d, h := definition(), household()
		edit(&d, &h)
		if p := make_(t, d, h); p.ID == first.ID {
			t.Errorf("%s: still plan %s", name, p.ID)
		}
	}
}

// The compose file and the manifest describe the same services: an image the manifest doesn't pin, or a pin for a
// service the compose file doesn't run, makes no plan.
func TestADefinitionWhoseComposeFileDisagreesMakesNoPlan(t *testing.T) {
	testenv.Isolate(t)
	extra := project()
	extra.Services = append(extra.Services, engine.Service{Name: "worker", Image: "example/worker@" + digestA})
	missing := project()
	missing.Services = missing.Services[1:]
	unpinned := project()
	unpinned.Services[0].Image = "postgres:16"
	short := project()
	short.Services[0].Image = "postgres:16@sha256:abc"
	trailing := project()
	trailing.Services[0].Image = "postgres:16@sha256:" + strings.Repeat("b", 64) + "-or-latest"
	for name, p := range map[string]engine.Project{"a service the manifest doesn't pin": extra, "a pin with no service": missing,
		"an image by tag": unpinned, "a digest too short": short, "a digest and more": trailing} {
		if _, err := plan.Make(definition(), official, p, household(), made); !errors.Is(err, plan.ErrDefinition) {
			t.Errorf("%s: %v, want ErrDefinition", name, err)
		}
	}
	for name, edit := range map[string]func(*catalog.App){
		"its name on a service it doesn't run": func(d *catalog.App) { d.Names.Service = "serverr" },
		"data in a service it doesn't run":     func(d *catalog.App) { d.Data[1].Service = "database" },
		"a health check on no service":         func(d *catalog.App) { d.Health[0].Service = "worker" },
		"an extra for no service": func(d *catalog.App) {
			d.Extras = []catalog.Extra{{ID: "lan", Kind: "network", Service: "worker", Why: "x"}}
		},
	} {
		d := definition()
		edit(&d)
		if _, err := plan.Make(d, official, project(), household(), made); !errors.Is(err, plan.ErrDefinition) {
			t.Errorf("%s: %v, want ErrDefinition", name, err)
		}
	}
}

// An app's name never takes one that the platform, the mesh, the web or mail already uses, a node's name, or another
// app's: the plan says why, and takes the next free one (#18, adversarial review round 2).
func TestAPlanNeverTakesAReservedName(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name, why, host string
	}{
		{"auth", "platform", "auth-2"}, {"admin", "platform", "admin-2"}, {"home", "platform", "home-2"},
		{"validation", "mesh", "validation-2"}, {"_acme-challenge", "dns", "acme-challenge"}, {"_dmarc", "dns", "dmarc"},
		{"www", "web", "www-2"}, {"mail", "mail", "mail-2"}, {"autodiscover", "mail", "autodiscover-2"},
		{"mta-sts", "mail", "mta-sts-2"}, {"study-pc", "node", "study-pc-2"}, {"films", "app", "films-2"},
	} {
		d, h := definition(), household()
		d.Names.Name = c.name
		h.Installed = []plan.Installed{{App: "jellyfin", Name: "films"}}
		got := make_(t, d, h).Name
		if got.Host != c.host+".ferreira.family" || got.Asked != c.name || got.Why != c.why {
			t.Errorf("%s: %s (asked %q, why %q), want %s because %s", c.name, got.Host, got.Asked, got.Why, c.host, c.why)
		}
	}
	d, h := definition(), household()
	d.Names.Name = "photos"
	h.Installed = []plan.Installed{{App: "immich", Name: "photos"}, {App: "photoprism", Name: "photos-2"}}
	if got := make_(t, d, h).Name; got.Host != "photos-3.ferreira.family" || got.Why != "app" {
		t.Errorf("a third photo app: %+v, want photos-3", got)
	}
	if got := make_(t, definition(), household()).Name; got.Asked != "" || got.Why != "" {
		t.Errorf("a free name: %+v, want no reason", got)
	}
}
