package compose

import (
	"context"
	"errors"
	"fmt"
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
	ErrNotPlain = errors.New("an app's compose file is one plain file: no include, extends, env_file or label_file")
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

// plain refuses a compose file that would make the loader read another file.
func plain(file []byte) error {
	var doc struct {
		Include  any                       `yaml:"include"`
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(file, &doc); err != nil {
		return fmt.Errorf("the compose file: %w", err)
	}
	if doc.Include != nil {
		return ErrNotPlain
	}
	for _, s := range doc.Services {
		for _, k := range []string{"extends", "env_file", "label_file"} {
			if _, ok := s[k]; ok {
				return ErrNotPlain
			}
		}
	}
	return nil
}
