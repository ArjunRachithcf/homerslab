package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/docker/compose/v5/pkg/api"
	"go.yaml.in/yaml/v3"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

var projectName = regexp.MustCompile(`^` + engine.ProjectPrefix + `[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// The ways a read is refused.
var (
	ErrNotOurs  = errors.New("not one of HomersLab's compose project names (homerslab-<app>)")
	ErrNotPlain = errors.New("an app's compose file is one plain file: no include, extends, env_file, label_file, profiles or aliases, nested at most 64 deep, at most 64 KiB")
)

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
	if !projectName.MatchString(name) {
		return engine.Project{}, ErrNotOurs
	}
	if err := plain(file); err != nil {
		return engine.Project{}, err
	}
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
		},
	})
	if err != nil {
		return engine.Project{}, fmt.Errorf("the compose file: %s", strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), ""))
	}
	out := engine.Project{Name: p.Name, Services: []engine.Service{}}
	for _, s := range p.Services {
		out.Services = append(out.Services, engine.Service{Name: s.Name, Image: s.Image})
	}
	slices.SortFunc(out.Services, func(a, b engine.Service) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// The bounds of a plain compose file: the agent parses it as root, so a small file must not make it work hard.
const (
	maxFileBytes = 64 << 10
	maxDepth     = 64
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
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
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
		for i := 1; i < len(services.Content); i += 2 {
			for _, k := range []string{"extends", "env_file", "label_file", "profiles"} {
				if value(services.Content[i], k) != nil {
					return fmt.Errorf("%w: %s", ErrNotPlain, k)
				}
			}
		}
	}
}

// shallow refuses flow nesting ([ and {) deeper than maxDepth before the YAML parser walks it: brackets in quotes and
// comments don't count. walk checks every kind of nesting once the file is parsed.
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

// walk refuses aliases and nesting deeper than maxDepth.
func walk(n *yaml.Node, depth int) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: an alias (*%s)", ErrNotPlain, n.Value)
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
