package compose

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

// runtimeOptions make the engine's service: no dry run, so no dry run with a stream either (ErrDryRunWithStreams), and
// no stream, since it only reads.
var runtimeOptions = options{}

// Engine reads HomersLab's running compose projects through the SDK, on one engine endpoint: their containers' state and
// health, many projects at once, and their logs. It changes nothing: the app's unit runs and stops the project with the
// standard compose command (ADR 0014).
type Engine struct {
	svc api.Compose
	api client.APIClient
}

// NewEngine is the engine at host (the environment's when ""). Making it reaches no engine.
func NewEngine(host string) (*Engine, error) {
	c, err := newCLI(host)
	if err != nil {
		return nil, err
	}
	svc, err := newService(c, runtimeOptions)
	if err != nil {
		return nil, err
	}
	return &Engine{svc: svc, api: c.Client()}, nil
}

// ErrEngine is an engine that didn't answer.
var ErrEngine = errors.New("the container engine didn't answer")

// maxAsks is how many projects' containers are asked at once.
const maxAsks = 4

// States are every project of HomersLab's (homerslab-<app>) that the engine has a container of, running or not, by
// name: one listing of the projects, then each one's containers, a few at once.
func (e *Engine) States(ctx context.Context) (map[string]engine.State, error) {
	stacks, err := e.svc.List(ctx, api.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEngine, err)
	}
	var names []string
	for _, s := range stacks {
		if OurName(s.Name) {
			names = append(names, s.Name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	out := make(map[string]engine.State, len(names))
	var mu sync.Mutex
	var errs []error
	sem := make(chan struct{}, maxAsks)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			cs, err := e.svc.Ps(ctx, name, api.PsOptions{All: true})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			st := engine.State{Name: name, Containers: []engine.Container{}}
			for _, c := range cs {
				st.Containers = append(st.Containers, engine.Container{Service: c.Service, Name: c.Name, State: string(c.State), Health: string(c.Health)})
			}
			slices.SortFunc(st.Containers, func(a, b engine.Container) int { return strings.Compare(a.Name, b.Name) })
			out[name] = st
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrEngine, errors.Join(errs...))
	}
	return out, nil
}

// Mounts are each service's volumes and folders of the machine, from its first container by name, as the engine inspects
// it: where they are inside the container, and on the machine. A tmpfs has no place on the machine, so it isn't one.
func (e *Engine) Mounts(ctx context.Context, name string) (map[string][]engine.Mount, error) {
	if !OurName(name) {
		return nil, ErrNotOurs
	}
	cs, err := e.svc.Ps(ctx, name, api.PsOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEngine, err)
	}
	slices.SortFunc(cs, func(a, b api.ContainerSummary) int { return strings.Compare(a.Name, b.Name) })
	out := map[string][]engine.Mount{}
	for _, c := range cs {
		if _, done := out[c.Service]; done {
			continue
		}
		r, err := e.api.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrEngine, err)
		}
		ms := []engine.Mount{}
		for _, m := range r.Container.Mounts {
			if m.Source != "" && (m.Type == mount.TypeVolume || m.Type == mount.TypeBind) {
				ms = append(ms, engine.Mount{Destination: m.Destination, Source: m.Source, Volume: m.Type == mount.TypeVolume, Name: m.Name, Driver: m.Driver})
			}
		}
		out[c.Service] = ms
	}
	return out, nil
}

// MaxLogLines is the most lines Logs gives.
const MaxLogLines = 1000

// LogLine is one line a container wrote.
type LogLine struct {
	Container string `json:"container"`
	Line      string `json:"line"`
	Err       bool   `json:"err,omitempty"` // it wrote it to its standard error
}

// Logs are a project's last lines, at most tail per container (and MaxLogLines in all), in the order the engine gave
// them.
func (e *Engine) Logs(ctx context.Context, name string, tail int) ([]LogLine, error) {
	if !OurName(name) {
		return nil, ErrNotOurs
	}
	tail = min(max(tail, 1), MaxLogLines)
	c := &logs{max: MaxLogLines}
	if err := e.svc.Logs(ctx, name, c, api.LogOptions{Tail: strconv.Itoa(tail)}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEngine, err)
	}
	return c.lines, nil
}

// logs collects a project's lines, up to max.
type logs struct {
	mu    sync.Mutex
	max   int
	lines []LogLine
}

func (l *logs) add(container, message string, isErr bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) >= l.max {
		l.lines = l.lines[1:]
	}
	l.lines = append(l.lines, LogLine{Container: container, Line: strings.TrimRight(message, "\r\n"), Err: isErr})
}

func (l *logs) Log(container, message string) { l.add(container, message, false) }
func (l *logs) Err(container, message string) { l.add(container, message, true) }
func (l *logs) Status(string, string)         {}
