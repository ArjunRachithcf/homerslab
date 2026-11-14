package catalog_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func no() *bool { b := false; return &b }

// The manifest holds every field the spec lists (spec: The app definition): identity, versions, sign-in methods,
// names and exposure, audience, data, secrets, health checks, resources, extras, the host-app recipe, update notes
// and the tier.
func TestTheLoaderReadsEveryFieldOfTheManifest(t *testing.T) {
	testenv.Isolate(t)
	c, err := catalog.Load(os.DirFS("testdata/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "test" || c.Name != "The test catalog" || c.Publisher != "HomersLab's tests" || len(c.Categories) != 2 {
		t.Errorf("the catalog: %+v", c)
	}
	got, ok := c.App("everything")
	if !ok {
		t.Fatal("no app everything")
	}
	want := catalog.App{
		ID: "everything", Name: "Everything", Purpose: "Photos", Description: "An app that uses every field of the manifest.",
		Category: "photos", Tier: catalog.Verified, Icon: "icon.svg",
		Screenshots: []catalog.Screenshot{{File: "screenshots/one.png", Caption: "The first screen"}},
		Versions: catalog.Versions{App: "v1.2.3", Images: []catalog.Image{{Service: "web", Digests: map[catalog.Engine]map[catalog.Arch]string{
			catalog.Docker: {catalog.X86_64: "sha256:" + strings.Repeat("1", 64), catalog.ARM64: "sha256:" + strings.Repeat("2", 64)},
			catalog.Podman: {catalog.X86_64: "sha256:" + strings.Repeat("1", 64)},
		}}}},
		SignIn: []catalog.SignIn{
			{Method: catalog.OIDC, RedirectURIs: []string{"/auth/callback", "app.everything:///callback"}, Scopes: []string{"openid", "email", "profile"}},
			{Method: catalog.LDAP, Attributes: []string{"uid", "mail"}},
			{Method: catalog.Guard},
		},
		Names:    catalog.Names{Name: "everything", Service: "web", Port: 8080, Exposure: "mesh", Public: true},
		Audience: catalog.Audience{Default: "adults", KidView: "Only the kids' albums"},
		Data: []catalog.Data{
			{What: "The library", Service: "web", Path: "/data"},
			{What: "Its database", Service: "web", Dump: &catalog.Dump{Command: []string{"pg_dump", "--username=postgres", "everything"}, File: "everything.sql"}},
			{What: "Thumbnails", Service: "web", Path: "/cache", Backup: no(), Why: "They are made again from the library"},
		},
		Secrets: []catalog.Secret{{Name: "db-password", What: "the database password", UID: 999}},
		Health:  []catalog.Health{{Service: "web", HTTP: &catalog.HTTPHealth{Port: 8080, Path: "/health"}}, {Service: "web", Compose: true}},
		Resources: catalog.Resources{MemoryMiB: 2048, DiskGiB: 10, Grows: "the library", CanPause: true,
			GPU: catalog.GPU{Need: "optional", Vendors: []string{"nvidia", "intel"}, For: "faster thumbnails"}},
		Extras: []catalog.Extra{
			{ID: "render", Kind: "device", Service: "web", Device: "/dev/dri/renderD128", Why: "Video made smaller on the GPU", Optional: true},
			{ID: "lan", Kind: "network", Service: "web", Why: "It finds the TVs at home"},
			{ID: "admin", Kind: "capability", Service: "web", Capability: "NET_ADMIN", Why: "It sets up its own network"},
		},
		UpdateNotes: []catalog.UpdateNote{{Version: "v1.2.3", Notes: "The first version in the catalog."}},
	}
	if !strings.HasPrefix(string(got.Compose), "services:") {
		t.Errorf("its compose file: %q", got.Compose)
	}
	if !strings.HasPrefix(got.Digest, "sha256:") {
		t.Errorf("its digest: %q", got.Digest)
	}
	got.Compose, got.Digest = nil, "" // TestADefinitionsDigestCoversEveryFileItIsMadeOf
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("loaded\n%s\nwant\n%s", g, w)
	}

	host, _ := c.App("vendor")
	wantHost := &catalog.HostApp{Packages: []catalog.HostPackage{{Distros: []string{"fedora"},
		Repository: "https://downloads.example.com/repo/rpm/", Key: "https://downloads.example.com/key.asc", Package: "vendor-server"}}}
	if !reflect.DeepEqual(host.Host, wantHost) || host.Compose != nil {
		t.Errorf("the host app: %+v, compose %q", host.Host, host.Compose)
	}
}

// fixture is the test catalog with one change: a file replaced (nil removes it), or text replaced in a file.
func fixture(t *testing.T, edit func(fstest.MapFS)) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS("testdata/catalog"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(os.DirFS("testdata/catalog"), p)
		m[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	edit(m)
	return m
}

func replace(file, old, new string) func(fstest.MapFS) {
	return func(m fstest.MapFS) {
		s := string(m[file].Data)
		if !strings.Contains(s, old) {
			panic("the fixture has no " + old)
		}
		m[file].Data = []byte(strings.Replace(s, old, new, 1))
	}
}

func remove(file string) func(fstest.MapFS) { return func(m fstest.MapFS) { delete(m, file) } }

const everything = "apps/everything/app.yaml"

// A definition that doesn't follow the schema is refused, naming the app and what's wrong.
func TestTheLoaderRefusesADefinitionOutsideTheSchema(t *testing.T) {
	testenv.Isolate(t)
	for name, c := range map[string]struct {
		edit func(fstest.MapFS)
		says string
	}{
		"an unknown field":               {replace(everything, "tier: verified", "tier: verified\nprivileged: true"), "privileged"},
		"no id":                          {replace(everything, "id: everything\n", ""), "id is required"},
		"an id that isn't its folder":    {replace(everything, "id: everything", "id: other"), "folder"},
		"a name with capitals":           {replace(everything, "name: everything", "name: Everything"), "names.name"},
		"no name":                        {replace(everything, "name: Everything\n", ""), "name is required"},
		"a name YAML reads as lines":     {replace(everything, "name: Everything\n", "name: \"X\\N---\\Nservices: {web: {privileged: true}}\\N#\"\n"), "name: one line"},
		"a name with a line separator":   {replace(everything, "name: Everything\n", "name: \"X\\Linclude: [/etc/shadow]\"\n"), "name: one line"},
		"a purpose with a control":       {replace(everything, "purpose: Photos", "purpose: \"Pho\\x07tos\""), "purpose: one line"},
		"no description":                 {replace(everything, "description: An app that uses every field of the manifest.\n", ""), "description is required"},
		"an unknown category":            {replace(everything, "category: photos", "category: games"), "category"},
		"an unknown tier":                {replace(everything, "tier: verified", "tier: gold"), "tier"},
		"no icon file":                   {remove("apps/everything/icon.svg"), "icon.svg"},
		"no screenshot file":             {remove("apps/everything/screenshots/one.png"), "screenshots/one.png"},
		"a path out of its folder":       {replace(everything, "icon: icon.svg", "icon: ../vendor/icon.svg"), "icon"},
		"no version":                     {replace(everything, "app: v1.2.3", "app: \"\""), "versions.app"},
		"an image without digests":       {replace(everything, "      digests:\n", "      digests: {}\n      x:\n"), "x"},
		"a digest that isn't one":        {replace(everything, "arm64: sha256:2222", "arm64: latest2222"), "digest"},
		"an unknown engine":              {replace(everything, "        podman:", "        lxc:"), "engine"},
		"an unknown architecture":        {replace(everything, "          arm64:", "          riscv:"), "architecture"},
		"one service pinned twice":       {replace(everything, "    - service: web\n", "    - service: web\n      digests: {docker: {x86_64: sha256:3333333333333333333333333333333333333333333333333333333333333333}}\n    - service: web\n"), "twice"},
		"no sign-in method":              {replace(everything, "  - method: oidc\n    redirectUris: [/auth/callback, app.everything:///callback]\n    scopes: [openid, email, profile]\n  - method: ldap\n    attributes: [uid, mail]\n  - method: guard\n", "  []\n"), "signIn"},
		"an unknown sign-in method":      {replace(everything, "method: ldap", "method: saml"), "signIn"},
		"a sign-in method twice":         {replace(everything, "method: ldap", "method: oidc"), "signIn"},
		"a name for the sign-in":         {replace(everything, "  name: everything\n", "  name: auth\n"), "reserved (platform)"},
		"a name for Admin":               {replace(everything, "  name: everything\n", "  name: admin\n"), "reserved (platform)"},
		"a name for Home":                {replace(everything, "  name: everything\n", "  name: home\n"), "reserved (platform)"},
		"NetBird's validation name":      {replace(everything, "  name: everything\n", "  name: validation\n"), "reserved (mesh)"},
		"the web's name":                 {replace(everything, "  name: everything\n", "  name: www\n"), "reserved (web)"},
		"mail's name":                    {replace(everything, "  name: everything\n", "  name: mail\n"), "reserved (mail)"},
		"mail clients' setup name":       {replace(everything, "  name: everything\n", "  name: autodiscover\n"), "reserved (mail)"},
		"mail's policy name":             {replace(everything, "  name: everything\n", "  name: mta-sts\n"), "reserved (mail)"},
		"a DNS service name":             {replace(everything, "  name: everything\n", "  name: _dmarc\n"), "reserved (dns)"},
		"no port for its name":           {replace(everything, "  port: 8080\n", ""), "names.port"},
		"an exposure that isn't mesh":    {replace(everything, "exposure: mesh", "exposure: public"), "exposure"},
		"an unknown audience":            {replace(everything, "default: adults", "default: neighbours"), "audience"},
		"data with no path or dump":      {replace(everything, "{what: The library, service: web, path: /data}", "{what: The library, service: web}"), "data"},
		"a folder inside a volume":       {replace(everything, "service: web, path: /data}", "service: web, path: /data/sub}"), "exactly one of its service's named volumes"},
		"a folder in no volume":          {replace(everything, "service: web, path: /data}", "service: web, path: /srv}"), "exactly one of its service's named volumes"},
		"a folder in a bind mount":       {replace("apps/everything/compose.yaml", "      - library:/data", "      - ./library:/data"), "exactly one of its service's named volumes"},
		"a volume that binds /etc":       {replace("apps/everything/compose.yaml", "  library:\n", "  library: {driver_opts: {type: none, o: bind, device: /etc}}\n"), "the app's own"},
		"another app's volume":           {replace("apps/everything/compose.yaml", "  library:\n", "  library: {external: true, name: homerslab-immich_library}\n"), "the app's own"},
		"the sign-in's database volume":  {replace("apps/everything/compose.yaml", "  library:\n", "  library: {name: homerslab-authentik_database}\n"), "the app's own"},
		"a volume of another driver":     {replace("apps/everything/compose.yaml", "  library:\n", "  library: {driver: rclone}\n"), "the app's own"},
		"a path that isn't plain":        {replace(everything, "service: web, path: /data}", "service: web, path: /data/../etc}"), "a plain absolute path"},
		"a dump through a shell":         {replace(everything, "command: [pg_dump, --username=postgres, everything]", "command: [sh, -c, \"pg_dump | nc x 80\"]"), "plain arguments"},
		"a dump's file out of its place": {replace(everything, "file: everything.sql", "file: ../everything.sql"), "plain name"},
		"data not backed up, no why":     {replace(everything, ", why: They are made again from the library", ""), "why"},
		"a secret with no name":          {replace(everything, "name: db-password, ", ""), "secrets"},
		"no health check":                {replace(everything, "health:\n  - service: web\n    http: {port: 8080, path: /health}\n  - {service: web, compose: true}\n", "health: []\n"), "health"},
		"no memory":                      {replace(everything, "memoryMiB: 2048", "memoryMiB: 0"), "memoryMiB"},
		"an unknown GPU need":            {replace(everything, "need: optional", "need: maybe"), "gpu"},
		"an unknown GPU maker":           {replace(everything, "vendors: [nvidia, intel]", "vendors: [nvidia, voodoo]"), "gpu"},
		"an unknown extra":               {replace(everything, "kind: capability", "kind: privileged"), "extras"},
		"an extra with no reason":        {replace(everything, ", why: It finds the TVs at home", ""), "extras"},
		"no compose file":                {remove("apps/everything/compose.yaml"), "compose.yaml"},
		"a host app with compose":        {func(m fstest.MapFS) { m["apps/vendor/compose.yaml"] = &fstest.MapFile{Data: []byte("services: {}")} }, "compose.yaml"},
		"a host app with no package":     {replace("apps/vendor/app.yaml", "      package: vendor-server\n", ""), "host"},
		"a host app with no packages":    {replace("apps/vendor/app.yaml", "  packages:\n    - distros: [fedora]\n      repository: https://downloads.example.com/repo/rpm/\n      key: https://downloads.example.com/key.asc\n      package: vendor-server\n", "  packages: []\n"), "host: at least"},
		"a second manifest in app.yaml":  {func(m fstest.MapFS) { m[everything].Data = append(m[everything].Data, []byte("---\nid: other\n")...) }, "one document"},
		"no catalog file":                {remove("catalog.yaml"), "catalog.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := catalog.Load(fixture(t, c.edit))
			if err == nil {
				t.Fatal("loaded")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("%v, want it to name %q", err, c.says)
			}
		})
	}
}

// A definition's digest covers its manifest, its compose file and its catalog, so a plan can say exactly what it
// was made from.
func TestADefinitionsDigestCoversEveryFileItIsMadeOf(t *testing.T) {
	testenv.Isolate(t)
	digest := func(edit func(fstest.MapFS)) string {
		t.Helper()
		c, err := catalog.Load(fixture(t, edit))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := c.App("everything")
		return a.Digest
	}
	first := digest(func(fstest.MapFS) {})
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("digest %q", first)
	}
	if again := digest(func(fstest.MapFS) {}); again != first {
		t.Errorf("the same files gave %s, then %s", first, again)
	}
	for name, edit := range map[string]func(fstest.MapFS){
		"the compose file's environment": replace("apps/everything/compose.yaml", "    image:", "    environment: {TZ: UTC}\n    image:"),
		"the manifest":                   replace(everything, "notes: The first version in the catalog.", "notes: Another note."),
		"the catalog":                    replace("catalog.yaml", "name: The test catalog", "name: Another name"),
	} {
		if d := digest(edit); d == first {
			t.Errorf("a change to %s kept the digest", name)
		}
	}
}
