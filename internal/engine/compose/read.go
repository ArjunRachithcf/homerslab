package compose

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"go.yaml.in/yaml/v4" // the parser Compose itself uses, so that both read merge keys and tags alike

	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

var projectName = regexp.MustCompile(`^` + engine.ProjectPrefix + `[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// The ways a read is refused.
var (
	ErrNotOurs  = errors.New("not one of HomersLab's compose project names (homerslab-<app>)")
	ErrNotPlain = errors.New("an app's compose file is one plain file: no include, extends, env_file, label_file, profiles, aliases or merge keys; at most 64 KiB, nested at most 64 deep, 64 services and 128 references between them")
	// ErrOffFloor is a project the floor refuses outright (spec: The floor): one that shares the host's namespaces.
	ErrOffFloor = errors.New("not allowed for an app")
)

// OurName says whether name is one of HomersLab's compose project names, homerslab-<app>.
func OurName(name string) bool { return projectName.MatchString(name) }

// readerOptions make the reader's service: a dry run with no stream, so nothing it is asked can change an engine.
var readerOptions = options{dryRun: true}

// Reader reads apps' compose projects through the SDK, for planning.
type Reader struct{ svc api.Compose }

// NewReader makes a reader for the engine at host (the environment's when ""). Reading reaches no engine.
func NewReader(host string) (*Reader, error) {
	c, err := newCLI(host)
	if err != nil {
		return nil, err
	}
	svc, err := newService(c, readerOptions)
	if err != nil {
		return nil, err
	}
	return &Reader{svc: svc}, nil
}

// Read loads one compose file as the project name, as the standard compose command would run it, and says which
// services it runs with which images. The file is read as written: no variable is filled in, so nothing of the
// reader's own environment reaches it, and it may name no other file.
func (r *Reader) Read(ctx context.Context, name string, file []byte) (engine.Project, error) {
	if !OurName(name) {
		return engine.Project{}, ErrNotOurs
	}
	if err := plain(file); err != nil {
		return engine.Project{}, err
	}
	return r.load(ctx, name, file)
}

// load loads a file that plain() passed. It follows no extends and no include, so it reads no other file even if
// plain() missed one, and it refuses a project with a service it extends or one that a profile leaves out.
func (r *Reader) load(ctx context.Context, name string, file []byte) (engine.Project, error) {
	dir, err := os.MkdirTemp("", "homerslab-read-")
	if err != nil {
		return engine.Project{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		return engine.Project{}, err
	}
	p, err := r.svc.LoadProject(ctx, api.ProjectLoadOptions{
		ProjectName: name,
		ConfigPaths: []string{path},
		WorkingDir:  dir,
		Offline:     true,
		ProjectOptionsFns: []cli.ProjectOptionsFn{
			cli.WithInterpolation(false),
			cli.WithoutEnvironmentResolution,
			cli.WithoutLabelsResolution,
			cli.WithLoadOptions(func(o *loader.Options) { o.SkipInclude, o.SkipExtends = true, true }),
		},
	})
	if err != nil {
		return engine.Project{}, fmt.Errorf("the compose file: %s", strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), ""))
	}
	if len(p.DisabledServices) > 0 {
		return engine.Project{}, fmt.Errorf("%w: a service only a profile runs", ErrNotPlain)
	}
	out := engine.Project{Name: p.Name, Services: []engine.Service{}}
	for name := range p.Secrets {
		out.Secrets = append(out.Secrets, name)
	}
	slices.Sort(out.Secrets)
	for _, s := range p.Services {
		if s.Extends != nil {
			return engine.Project{}, fmt.Errorf("%w: %s extends another service", ErrNotPlain, s.Name)
		}
		if err := ownNamespaces(s); err != nil {
			return engine.Project{}, err
		}
		svc := engine.Service{Name: s.Name, Image: s.Image, NetworkMode: s.NetworkMode}
		if s.NetworkMode == "" {
			for n := range s.Networks {
				if n != "default" || len(s.Networks) > 1 {
					svc.Networks = append(svc.Networks, n)
				}
			}
			slices.Sort(svc.Networks)
		}
		for _, port := range s.Ports {
			if port.Published == "" {
				// Compose publishes a port without a host port at one the engine picks.
				svc.Published = append(svc.Published, fmt.Sprintf("%d/%s", port.Target, cmp.Or(port.Protocol, "tcp")))
				continue
			}
			pub := port.Published + ":" + strconv.Itoa(int(port.Target))
			if port.HostIP != "" {
				pub = port.HostIP + ":" + pub
			}
			svc.Published = append(svc.Published, pub+"/"+cmp.Or(port.Protocol, "tcp"))
		}
		for _, v := range s.Volumes {
			if v.Type == "bind" && v.Bind != nil && v.Bind.SELinux != "" {
				svc.Relabelled = append(svc.Relabelled, v.Source)
			}
			if v.Type == "volume" && v.Source != "" {
				svc.Volumes = append(svc.Volumes, v.Target)
			}
		}
		slices.Sort(svc.Volumes)
		svc.Loosened = loosened(s)
		for name := range s.ExtraHosts {
			svc.Hosts = append(svc.Hosts, strings.ToLower(name))
		}
		slices.Sort(svc.Hosts)
		out.Services = append(out.Services, svc)
	}
	slices.SortFunc(out.Services, func(a, b engine.Service) int { return strings.Compare(a.Name, b.Name) })
	for key, n := range p.Networks {
		nw := engine.Network{Name: key, Driver: n.Driver}
		switch {
		case bool(n.External):
			nw.Joined = cmp.Or(n.Name, key)
		case n.Name != "" && n.Name != p.Name+"_"+key:
			nw.Joined = n.Name
		}
		nw.Configured = len(n.DriverOpts) > 0 || n.Ipam.Driver != "" || len(n.Ipam.Config) > 0 || len(n.Ipam.Options) > 0
		out.Networks = append(out.Networks, nw)
	}
	slices.SortFunc(out.Networks, func(a, b engine.Network) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// tightens says whether a security option only takes from a container: no-new-privileges, the one the floor's
// hardening uses. Any other (a label, seccomp or AppArmor profile, unconfined system paths) is the engine's to set.
func tightens(o string) bool {
	switch strings.ReplaceAll(o, "=", ":") {
	case "no-new-privileges", "no-new-privileges:true":
		return true
	}
	return false
}

// loosened is what a service asks for that loosens its container's confinement, each as its compose key says it: what
// the floor and the extras speak of (privileged; a device or a capability, until extras are approved) and what would
// undo the app's own level or limits. The namespaces are the floor's, refused as the project is read (ownNamespaces).
func loosened(s types.ServiceConfig) []string {
	var out []string
	add := func(on bool, what string) {
		if on {
			out = append(out, what)
		}
	}
	add(s.Privileged, "privileged")
	add(len(s.CapAdd) > 0, "cap_add")
	add(len(s.Devices) > 0, "devices")
	add(s.Deploy != nil && s.Deploy.Resources.Reservations != nil && len(s.Deploy.Resources.Reservations.Devices) > 0, "deploy.resources.reservations.devices")
	add(len(s.Gpus) > 0, "gpus")
	add(slices.ContainsFunc(s.SecurityOpt, func(o string) bool { return !tightens(o) }), "security_opt")
	add(s.CgroupParent != "", "cgroup_parent")
	add(s.Runtime != "", "runtime")
	add(len(s.Sysctls) > 0, "sysctls")
	add(slices.ContainsFunc(s.VolumesFrom, func(v string) bool { return strings.HasPrefix(v, "container:") }), "volumes_from")
	return out
}

// namespaces are the values a service's namespace keys may have, each a namespace of the app's own: its own
// (Compose's default, ""), or for some keys a named kind of its own, or a service's of the same project ("service:",
// sharing), which is held to the same rule. Anything else is refused, the host's above all: with pid: host an app sees
// the node's processes and, at a uid it shares with one, signals or traces it; ipc: host gives it the node's shared
// memory, uts: host its host name, userns_mode: host its users where the engine remaps them, cgroup: host its cgroups.
// So is a container's by name (container:), which may be the host's, and Podman's paths (ns:), which may be too. The
// spec's extras are the machine's network, a device and a capability: these are none of them, so no extra allows
// them (#190).
var namespaces = map[string]struct {
	own     []string
	service bool
}{
	"pid":         {own: []string{""}, service: true},
	"ipc":         {own: []string{"", "private", "shareable", "none"}, service: true},
	"uts":         {own: []string{""}},
	"userns_mode": {own: []string{""}},
	"cgroup":      {own: []string{"", "private"}},
}

// ownNamespaces refuses a service that shares a namespace that isn't the app's own (namespaces).
func ownNamespaces(s types.ServiceConfig) error {
	for _, n := range []struct{ key, v string }{{"pid", s.Pid}, {"ipc", s.Ipc}, {"uts", s.Uts}, {"userns_mode", s.UserNSMode}, {"cgroup", s.Cgroup}} {
		key, v := n.key, n.v
		allowed := namespaces[key]
		if slices.Contains(allowed.own, v) || (allowed.service && strings.HasPrefix(v, types.ServicePrefix)) {
			continue
		}
		return fmt.Errorf("%w: %s's %s is %q, a namespace that isn't the app's own", ErrOffFloor, s.Name, key, v)
	}
	return nil
}

// The bounds of a plain compose file: the agent parses it as root, so a small file must not make it work hard. Size,
// nesting, services and references are capped; so are the paths of references (maxPaths) and port ranges, which
// Compose expands. These bound what was found to make Compose work hard; they don't prove that nothing else can (the
// resolution of services' pre_start hooks, for one, grows memory with the services, and isn't capped on its own),
// which is why each read also runs in a child that is killed at its deadline, under its own memory and CPU limits
// (agent.ReadProjectMain).
const (
	maxFileBytes  = 64 << 10
	maxDepth      = 64
	maxServices   = 64
	maxReferences = 128
)

// plain refuses a compose file that would make the loader read another file or work hard, in any of its YAML
// documents (the loader merges them all): one over 64 KiB, an alias (whose expansion a few hundred bytes can make
// billions of nodes), nesting deeper than 64, or a service the default run leaves out (profiles), which the plan
// wouldn't see.
func plain(file []byte) error {
	if len(file) > maxFileBytes {
		return fmt.Errorf("%w: over %d KiB", ErrNotPlain, maxFileBytes>>10)
	}
	if err := shallow(file); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(file))
	count, refs := 0, 0
	graph := map[string][]string{}
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return paths(graph)
		}
		if err != nil {
			return fmt.Errorf("the compose file: %w", err)
		}
		if err := walk(&doc, 0); err != nil {
			return err
		}
		root := &doc
		if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
			root = root.Content[0]
		}
		if v := value(root, "include"); v != nil {
			return fmt.Errorf("%w: include", ErrNotPlain)
		}
		services := value(root, "services")
		if services == nil || services.Kind != yaml.MappingNode {
			continue
		}
		if count += len(services.Content) / 2; count > maxServices {
			return fmt.Errorf("%w: more than %d services", ErrNotPlain, maxServices)
		}
		for i := 1; i < len(services.Content); i += 2 {
			s := services.Content[i]
			for _, k := range []string{"extends", "env_file", "label_file", "profiles"} {
				if value(s, k) != nil {
					return fmt.Errorf("%w: %s", ErrNotPlain, k)
				}
			}
			for k := range namespaces {
				if v := value(s, k); v != nil && (v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str") {
					return fmt.Errorf("%w: %s that isn't a string", ErrNotPlain, k)
				}
			}
			for _, k := range []string{"ports", "expose"} {
				if v := value(s, k); v != nil && hasRange(v) {
					return fmt.Errorf("%w: a port range (Compose makes one entry per port)", ErrNotPlain)
				}
			}
			deps := references(s)
			name := services.Content[i-1].Value
			graph[name] = append(graph[name], deps...)
			if refs += len(deps); refs > maxReferences {
				return fmt.Errorf("%w: more than %d references between services", ErrNotPlain, maxReferences)
			}
		}
	}
}

// shallow is an early exit: it refuses flow nesting ([ and {) deeper than maxDepth before the YAML parser walks it,
// so that a deep file costs nothing. Brackets in quotes and comments don't count, and it can be fooled; walk, on the
// parsed file, is the real check of every kind of nesting.
func shallow(file []byte) error {
	depth, quote, comment := 0, byte(0), false
	for i, c := range file {
		switch {
		case comment:
			comment = c != '\n'
		case quote != 0:
			if c == quote && (quote == '\'' || file[i-1] != '\\') {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			comment = true
		case c == '[' || c == '{':
			if depth++; depth > maxDepth {
				return fmt.Errorf("%w: nested deeper than %d", ErrNotPlain, maxDepth)
			}
		case c == ']' || c == '}':
			depth--
		}
	}
	return nil
}

// references are the services a service refers to: by depends_on, links and volumes_from, and by network_mode, ipc,
// pid and uts set to service:<name> (compose-go's normalize makes each of these a dependency).
func references(s *yaml.Node) []string {
	var out []string
	for _, k := range []string{"depends_on", "links", "volumes_from"} {
		v := value(s, k)
		switch {
		case v == nil:
		case v.Kind == yaml.MappingNode: // depends_on's long form: its keys
			for i := 0; i < len(v.Content); i += 2 {
				out = append(out, v.Content[i].Value)
			}
		case v.Kind == yaml.SequenceNode:
			for _, e := range v.Content {
				if strings.HasPrefix(e.Value, "container:") {
					continue
				}
				name, _, _ := strings.Cut(e.Value, ":") // links' alias, volumes_from's mode
				out = append(out, name)
			}
		default:
			out = append(out, v.Value)
		}
	}
	for _, k := range []string{"network_mode", "ipc", "pid", "uts"} {
		if v := value(s, k); v != nil {
			if name, ok := strings.CutPrefix(v.Value, "service:"); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// maxPaths bounds the paths of references, summed over the services: Compose walks the references of every service
// again for each service that reaches it, so its work grows with the paths, which few references can multiply (64
// services, each on the two before it, make Fibonacci-many). A chain of 64 has 2,080.
const maxPaths = 4096

// paths counts the paths of references from every service, and refuses past maxPaths, or a cycle.
func paths(graph map[string][]string) error {
	const visiting = -1
	memo := map[string]int{}
	var from func(string) (int, error)
	from = func(s string) (int, error) {
		switch n := memo[s]; {
		case n == visiting:
			return 0, fmt.Errorf("%w: services that refer to each other in a cycle", ErrNotPlain)
		case n > 0:
			return n, nil
		}
		memo[s] = visiting
		n := 1
		for _, d := range graph[s] {
			m, err := from(d)
			if err != nil {
				return 0, err
			}
			n = min(n+m, maxPaths+1)
		}
		memo[s] = n
		return n, nil
	}
	total := 0
	for s := range graph {
		n, err := from(s)
		if err != nil {
			return err
		}
		if total = min(total+n, maxPaths+1); total > maxPaths {
			return fmt.Errorf("%w: more than %d paths of references between services", ErrNotPlain, maxPaths)
		}
	}
	return nil
}

// hasRange says whether a ports or expose entry, or any value in it, is a range of ports.
func hasRange(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		return portRange.MatchString(n.Value)
	}
	for _, c := range n.Content {
		if hasRange(c) {
			return true
		}
	}
	return false
}

var portRange = regexp.MustCompile(`[0-9]\s*-\s*[0-9]`)

// walk refuses aliases, merge keys (<< and the !!merge tag, which merge one mapping into another with no alias, so
// keys plain() refuses could hide behind them) and nesting deeper than maxDepth.
func walk(n *yaml.Node, depth int) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: an alias (*%s)", ErrNotPlain, n.Value)
	}
	if n.ShortTag() == "!!merge" || (n.Kind == yaml.ScalarNode && n.Value == "<<" && n.Style == 0 && n.Tag == "") {
		return fmt.Errorf("%w: a merge key", ErrNotPlain)
	}
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		if depth++; depth > maxDepth {
			return fmt.Errorf("%w: nested deeper than %d", ErrNotPlain, maxDepth)
		}
	}
	for _, c := range n.Content {
		if err := walk(c, depth); err != nil {
			return err
		}
	}
	return nil
}

// value is a mapping node's value for key, or nil.
func value(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
