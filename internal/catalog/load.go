package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Load reads a catalog folder: catalog.yaml, then every app under apps/, in id order.
func Load(fsys fs.FS) (*Catalog, error) {
	var c Catalog
	if err := decode(fsys, "catalog.yaml", &c); err != nil {
		return nil, err
	}
	if c.ID == "" || c.Name == "" || c.Publisher == "" {
		return nil, errors.New("catalog.yaml: id, name and publisher are required")
	}
	categories := map[string]bool{}
	for _, k := range c.Categories {
		if !idPattern.MatchString(k.ID) || k.Name == "" {
			return nil, fmt.Errorf("catalog.yaml: category %q needs an id and a name", k.ID)
		}
		categories[k.ID] = true
	}
	dirs, err := fs.ReadDir(fsys, "apps")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		a, err := loadApp(fsys, d.Name(), categories)
		if err != nil {
			return nil, fmt.Errorf("app %s: %w", d.Name(), err)
		}
		c.Apps = append(c.Apps, a)
	}
	slices.SortFunc(c.Apps, func(a, b App) int { return strings.Compare(a.ID, b.ID) })
	return &c, nil
}

// App is the catalog's app with that id.
func (c *Catalog) App(id string) (App, bool) {
	i := slices.IndexFunc(c.Apps, func(a App) bool { return a.ID == id })
	if i < 0 {
		return App{}, false
	}
	return c.Apps[i], true
}

// decode reads one YAML file strictly: a field the schema doesn't know is an error.
func decode(fsys fs.FS, name string, v any) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func loadApp(fsys fs.FS, dir string, categories map[string]bool) (App, error) {
	var a App
	if err := decode(fsys, path.Join("apps", dir, "app.yaml"), &a); err != nil {
		return a, err
	}
	p := problems{}
	switch {
	case a.ID == "":
		p.add("id is required")
	case a.ID != dir:
		p.add("id %q isn't its folder's name, %q", a.ID, dir)
	case !idPattern.MatchString(a.ID):
		p.add("id %q: lowercase letters, digits and dashes", a.ID)
	}
	p.need(a.Name, "name")
	p.need(a.Purpose, "purpose")
	p.need(a.Description, "description")
	if !categories[a.Category] {
		p.add("category %q isn't one of the catalog's", a.Category)
	}
	if a.Tier != Verified && a.Tier != Community {
		p.add("tier %q: verified or community", a.Tier)
	}
	file := func(field, name string) {
		clean := path.Clean(name)
		if name == "" || path.IsAbs(name) || clean == "." || strings.HasPrefix(clean, "..") {
			p.add("%s %q: a file in the app's folder", field, name)
			return
		}
		if fi, err := fs.Stat(fsys, path.Join("apps", dir, clean)); err != nil || fi.IsDir() {
			p.add("%s: no file %s", field, name)
		}
	}
	file("icon", a.Icon)
	for _, s := range a.Screenshots {
		file("screenshots", s.File)
		p.need(s.Caption, "screenshots' caption")
	}
	checkVersions(&p, a.Versions)
	checkSignIn(&p, a.SignIn)
	checkNames(&p, a.Names)
	switch a.Audience.Default {
	case "everyone", "adults", "admins":
	default:
		p.add("audience.default %q: everyone, adults or admins", a.Audience.Default)
	}
	checkData(&p, a.Data)
	for _, s := range a.Secrets {
		if !idPattern.MatchString(s.Name) || s.What == "" || s.UID < 0 {
			p.add("secrets: %q needs a name, what it is, and the UID that reads it", s.Name)
		}
	}
	if len(a.Health) == 0 {
		p.add("health: at least one check")
	}
	for _, h := range a.Health {
		if h.Service == "" || (h.HTTP == nil) == !h.Compose || (h.HTTP != nil && (h.HTTP.Port <= 0 || !strings.HasPrefix(h.HTTP.Path, "/"))) {
			p.add("health: each check is a service's http port and path, or its compose healthcheck")
		}
	}
	checkResources(&p, a.Resources)
	checkExtras(&p, a.Extras)

	compose, err := fs.ReadFile(fsys, path.Join("apps", dir, "compose.yaml"))
	switch {
	case a.Host == nil && err != nil:
		p.add("compose.yaml: an app in containers needs its compose file")
	case a.Host != nil && err == nil:
		p.add("compose.yaml: a host app has none")
	case a.Host != nil:
		checkHost(&p, a.Host)
	default:
		a.Compose = compose
	}
	return a, p.err()
}

func checkVersions(p *problems, v Versions) {
	p.need(v.App, "versions.app")
	seen := map[string]bool{}
	for _, im := range v.Images {
		if seen[im.Service] {
			p.add("versions.images: %s pinned twice", im.Service)
		}
		seen[im.Service] = true
		if im.Service == "" || len(im.Digests) == 0 {
			p.add("versions.images: each names its service and its digests")
		}
		for e, arches := range im.Digests {
			if e != Docker && e != Podman {
				p.add("versions.images %s: engine %q: docker or podman", im.Service, e)
			}
			for arch, d := range arches {
				if arch != X86_64 && arch != ARM64 {
					p.add("versions.images %s: architecture %q: x86_64 or arm64", im.Service, arch)
				}
				if !digestPattern.MatchString(d) {
					p.add("versions.images %s: %q isn't a sha256 digest", im.Service, d)
				}
			}
		}
	}
}

func checkSignIn(p *problems, methods []SignIn) {
	if len(methods) == 0 {
		p.add("signIn: at least one method")
	}
	seen := map[SignInMethod]bool{}
	for _, m := range methods {
		switch m.Method {
		case OIDC, LDAP, Guard:
		default:
			p.add("signIn: method %q: oidc, ldap or guard", m.Method)
		}
		if seen[m.Method] {
			p.add("signIn: %s twice", m.Method)
		}
		seen[m.Method] = true
	}
}

// reserved are the names the platform, the mesh, the web and mail use on a household's domains (#18, adversarial
// review round 2): no app may take one.
var reserved = map[string]string{
	"auth": "platform", "admin": "platform", "home": "platform", // the sign-in, Admin and Home
	"validation": "mesh", // NetBird's check of a custom domain
	"www":        "web",
	"mail":       "mail", "autodiscover": "mail", "mta-sts": "mail",
}

// Reserved says what a name on the household's domains is reserved for: platform, mesh, dns (any name starting with
// "_"), web or mail; "" when it isn't.
func Reserved(name string) string {
	if strings.HasPrefix(name, "_") {
		return "dns"
	}
	return reserved[strings.ToLower(name)]
}

func checkNames(p *problems, n Names) {
	if !idPattern.MatchString(n.Name) {
		p.add("names.name %q: lowercase letters, digits and dashes", n.Name)
	}
	if why := Reserved(n.Name); why != "" {
		p.add("names.name %q is reserved (%s): no app may take it", n.Name, why)
	}
	p.need(n.Service, "names.service")
	if n.Port <= 0 || n.Port > 65535 {
		p.add("names.port: the port the edge sends people to")
	}
	if n.Exposure != "mesh" {
		p.add("names.exposure %q: every app starts on the mesh only", n.Exposure)
	}
}

func checkData(p *problems, data []Data) {
	for _, d := range data {
		if d.What == "" || d.Service == "" || (d.Path == "") == (d.Dump == nil) {
			p.add("data %q: what it is, its service, and a path or a dump", d.What)
		}
		if d.Dump != nil && (len(d.Dump.Command) == 0 || d.Dump.File == "") {
			p.add("data %q: a dump's command and the file it makes", d.What)
		}
		if !d.BackedUp() && d.Why == "" {
			p.add("data %q: why it isn't backed up", d.What)
		}
	}
}

func checkResources(p *problems, r Resources) {
	if r.MemoryMiB <= 0 {
		p.add("resources.memoryMiB: the memory it needs")
	}
	if r.DiskGiB <= 0 {
		p.add("resources.diskGiB: the disk it needs")
	}
	switch r.GPU.Need {
	case "none":
	case "optional", "required":
		if len(r.GPU.Vendors) == 0 || r.GPU.For == "" {
			p.add("resources.gpu: which makers' GPUs, and what for")
		}
	default:
		p.add("resources.gpu.need %q: none, optional or required", r.GPU.Need)
	}
	for _, v := range r.GPU.Vendors {
		if v != "nvidia" && v != "intel" && v != "amd" {
			p.add("resources.gpu.vendors %q: nvidia, intel or amd", v)
		}
	}
}

func checkExtras(p *problems, extras []Extra) {
	for _, x := range extras {
		ok := idPattern.MatchString(x.ID) && x.Service != "" && x.Why != ""
		switch x.Kind {
		case "network":
		case "device":
			ok = ok && strings.HasPrefix(x.Device, "/dev/")
		case "capability":
			ok = ok && x.Capability != ""
		default:
			ok = false
		}
		if !ok {
			p.add("extras %q: an id, its service, why, and a network, a device or a capability", x.ID)
		}
	}
}

func checkHost(p *problems, h *HostApp) {
	if len(h.Packages) == 0 {
		p.add("host: at least one package")
	}
	for _, pk := range h.Packages {
		if len(pk.Distros) == 0 || !strings.HasPrefix(pk.Repository, "https://") || !strings.HasPrefix(pk.Key, "https://") || pk.Package == "" {
			p.add("host: each package has its distros, its maker's repository and key over https, and its name")
		}
	}
}

// problems gathers everything wrong with one definition, so one load names them all.
type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p *problems) need(v, field string) {
	if strings.TrimSpace(v) == "" {
		p.add("%s is required", field)
	}
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New(strings.Join(slices.Compact(p), "; "))
}
