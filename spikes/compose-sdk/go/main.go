// Command spike exercises Docker Compose's Go SDK (github.com/docker/compose/v5) for HomersLab's
// container-engine adapter: load, up (detached, wait for healthy), ps and health, logs, down, dry run.
//
// Safety: it refuses any project whose name does not start with "hlspike-compose", so it can never
// act on another compose project of the engine it talks to.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
)

const guardPrefix = "hlspike-compose"

type options struct {
	host           string
	project        string
	files          string
	events         bool
	dryRun         bool
	streams        bool
	volumes        bool
	byName         bool
	noStartProject bool
	onExit         bool
	wait           bool
	waitTimeout    time.Duration
	timeout        time.Duration
	tail           string
	follow         bool
	recreate       string
}

func main() {
	var o options
	fs := flag.NewFlagSet("spike", flag.ExitOnError)
	fs.StringVar(&o.host, "host", "", "engine endpoint, e.g. unix:///run/user/1000/podman/podman.sock (default: DOCKER_HOST or the CLI's context)")
	fs.StringVar(&o.project, "p", guardPrefix, "project name (must start with "+guardPrefix+")")
	fs.StringVar(&o.files, "f", "", "compose files, comma-separated")
	fs.BoolVar(&o.events, "events", false, "print the SDK's progress events (EventProcessor)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "compose.WithDryRun")
	fs.BoolVar(&o.streams, "streams", false, "also pass compose.WithOutputStream/WithErrorStream")
	fs.BoolVar(&o.volumes, "v", false, "down: remove named and anonymous volumes")
	fs.BoolVar(&o.byName, "by-name", false, "down: don't load the files, act by project name only")
	fs.BoolVar(&o.noStartProject, "no-start-project", false, "up: leave StartOptions.Project nil")
	fs.BoolVar(&o.onExit, "on-exit", false, "up: set Start.OnExit=CascadeStop in detached mode (incompatible options)")
	fs.BoolVar(&o.wait, "wait", true, "up: Start.Wait")
	fs.DurationVar(&o.waitTimeout, "wait-timeout", 60*time.Second, "up: Start.WaitTimeout")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "overall context timeout")
	fs.StringVar(&o.tail, "tail", "20", "logs: lines per container")
	fs.BoolVar(&o.follow, "follow", false, "logs: follow until the context ends")
	fs.StringVar(&o.recreate, "recreate", api.RecreateDiverged, "up: CreateOptions.Recreate (diverged|force|never)")
	_ = fs.Parse(os.Args[1:])
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spike [flags] version|load|up|stop|start|ps|ls|inspect|logs|down|volumes")
		os.Exit(2)
	}
	if !strings.HasPrefix(o.project, guardPrefix) {
		fmt.Fprintf(os.Stderr, "refusing project %q: only %s* projects\n", o.project, guardPrefix)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	start := time.Now()
	err := run(ctx, fs.Arg(0), o)
	fmt.Printf("== %s took %s\n", fs.Arg(0), time.Since(start).Round(time.Millisecond))
	if err != nil {
		fmt.Printf("== ERROR %s\n", describe(err))
		os.Exit(1)
	}
}

// newCLI builds the docker/cli object the SDK needs, pointed at one engine.
func newCLI(host string) (*command.DockerCli, error) {
	cli, err := command.NewDockerCli()
	if err != nil {
		return nil, err
	}
	opts := flags.NewClientOptions()
	if host != "" {
		opts.Hosts = []string{host}
	}
	if err := cli.Initialize(opts); err != nil {
		return nil, err
	}
	return cli, nil
}

func newService(cli command.Cli, o options) (api.Compose, error) {
	var opts []compose.Option
	if o.events {
		opts = append(opts, compose.WithEventProcessor(&eventLog{w: os.Stdout}))
	}
	if o.dryRun {
		opts = append(opts, compose.WithDryRun)
	}
	if o.streams {
		opts = append(opts, compose.WithOutputStream(os.Stdout), compose.WithErrorStream(os.Stderr))
	}
	opts = append(opts, compose.WithPrompt(compose.AlwaysOkPrompt()))
	return compose.NewComposeService(cli, opts...)
}

func load(ctx context.Context, svc api.Compose, o options) (*types.Project, error) {
	if o.files == "" {
		return nil, errors.New("-f is required")
	}
	return svc.LoadProject(ctx, api.ProjectLoadOptions{
		ProjectName: o.project,
		ConfigPaths: strings.Split(o.files, ","),
	})
}

func run(ctx context.Context, cmd string, o options) error {
	cli, err := newCLI(o.host)
	if err != nil {
		return fmt.Errorf("docker cli: %w", err)
	}
	svc, err := newService(cli, o)
	if err != nil {
		return fmt.Errorf("compose service: %w", err)
	}
	switch cmd {
	case "version":
		return version(ctx, cli)
	case "load":
		p, err := load(ctx, svc, o)
		if err != nil {
			return err
		}
		printProject(p)
		return nil
	case "up":
		p, err := load(ctx, svc, o)
		if err != nil {
			return err
		}
		up := api.UpOptions{
			Create: api.CreateOptions{
				RemoveOrphans:        true,
				Recreate:             o.recreate,
				RecreateDependencies: api.RecreateDiverged,
				QuietPull:            true,
			},
			Start: api.StartOptions{Project: p, Wait: o.wait, WaitTimeout: o.waitTimeout},
		}
		if o.noStartProject {
			up.Start.Project = nil
		}
		if o.onExit {
			up.Start.OnExit = api.CascadeStop
		}
		before := containerIDs(ctx, cli, o.project)
		if err := svc.Up(ctx, p, up); err != nil {
			return err
		}
		after := containerIDs(ctx, cli, o.project)
		fmt.Printf("== containers before up: %d, after: %d, kept (same ID): %d\n", len(before), len(after), sameIDs(before, after))
		return ps(ctx, svc, o.project)
	case "ps":
		return ps(ctx, svc, o.project)
	case "ls":
		stacks, err := svc.List(ctx, api.ListOptions{All: true})
		if err != nil {
			return err
		}
		others := 0
		for _, s := range stacks {
			if strings.HasPrefix(s.Name, guardPrefix) {
				fmt.Printf("stack %s status=%q files=%s\n", s.Name, s.Status, s.ConfigFiles)
			} else {
				others++
			}
		}
		fmt.Printf("(%d other projects on this engine, not shown)\n", others)
		return nil
	case "inspect":
		return inspect(ctx, cli, svc, o.project)
	case "logs":
		return svc.Logs(ctx, o.project, logPrinter{w: os.Stdout}, api.LogOptions{Tail: o.tail, Follow: o.follow})
	case "stop":
		if err := svc.Stop(ctx, o.project, api.StopOptions{}); err != nil {
			return err
		}
		return ps(ctx, svc, o.project)
	case "start":
		start := api.StartOptions{Wait: o.wait, WaitTimeout: o.waitTimeout}
		if o.files != "" {
			p, err := load(ctx, svc, o)
			if err != nil {
				return err
			}
			start.Project = p
		}
		if err := svc.Start(ctx, o.project, start); err != nil {
			return err
		}
		return ps(ctx, svc, o.project)
	case "down":
		down := api.DownOptions{RemoveOrphans: true, Volumes: o.volumes}
		if !o.byName {
			p, err := load(ctx, svc, o)
			if err != nil {
				return err
			}
			down.Project = p
		}
		if err := svc.Down(ctx, o.project, down); err != nil {
			return err
		}
		return leftovers(ctx, cli, o.project)
	case "volumes":
		return leftovers(ctx, cli, o.project)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func version(ctx context.Context, cli *command.DockerCli) error {
	if bi, ok := debug.ReadBuildInfo(); ok {
		fmt.Printf("go %s\n", bi.GoVersion)
		for _, d := range bi.Deps {
			switch d.Path {
			case "github.com/docker/compose/v5", "github.com/docker/cli", "github.com/compose-spec/compose-go/v2",
				"github.com/moby/moby/client", "github.com/moby/moby/api", "github.com/docker/docker":
				fmt.Printf("dep %s %s\n", d.Path, d.Version)
			}
		}
	}
	fmt.Printf("api.ComposeVersion (com.docker.compose.version label) = %q\n", api.ComposeVersion)
	fmt.Printf("endpoint %s\n", cli.DockerEndpoint().Host)
	v, err := cli.Client().ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("engine %q version=%s api=%s (min %s) negotiated client api=%s os=%s\n",
		v.Platform.Name, v.Version, v.APIVersion, v.MinAPIVersion, cli.Client().ClientVersion(), v.Os)
	for _, c := range v.Components {
		fmt.Printf("  component %s %s\n", c.Name, c.Version)
	}
	return nil
}

func printProject(p *types.Project) {
	fmt.Printf("project %s workdir=%s files=%v\n", p.Name, p.WorkingDir, p.ComposeFiles)
	for _, name := range p.ServiceNames() {
		s := p.Services[name]
		fmt.Printf("  service %s image=%s\n", name, s.Image)
		for _, port := range s.Ports {
			fmt.Printf("    port %s:%s->%d/%s\n", port.HostIP, port.Published, port.Target, port.Protocol)
		}
		for _, v := range s.Volumes {
			sel := ""
			if v.Bind != nil && v.Bind.SELinux != "" {
				sel = " selinux=" + v.Bind.SELinux
			}
			fmt.Printf("    volume %s %s -> %s ro=%v%s\n", v.Type, v.Source, v.Target, v.ReadOnly, sel)
		}
		for _, sec := range s.Secrets {
			fmt.Printf("    secret %s target=%q\n", sec.Source, sec.Target)
		}
		if s.HealthCheck != nil {
			fmt.Printf("    healthcheck %v\n", s.HealthCheck.Test)
		}
		keys := make([]string, 0, len(s.CustomLabels))
		for k := range s.CustomLabels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    custom label %s=%s\n", k, s.CustomLabels[k])
		}
	}
	for name, v := range p.Volumes {
		fmt.Printf("  volume %s (engine name %s)\n", name, v.Name)
	}
	for name, s := range p.Secrets {
		// The secret's content is never printed: only where it comes from.
		fmt.Printf("  secret %s file=%s env=%q\n", name, s.File, s.Environment)
	}
	for name, n := range p.Networks {
		fmt.Printf("  network %s (engine name %s)\n", name, n.Name)
	}
}

func ps(ctx context.Context, svc api.Compose, project string) error {
	cs, err := svc.Ps(ctx, project, api.PsOptions{All: true})
	if err != nil {
		return err
	}
	fmt.Printf("%-28s %-8s %-9s %-10s %-4s %s\n", "NAME", "SERVICE", "STATE", "HEALTH", "EXIT", "PORTS")
	for _, c := range cs {
		var ports []string
		for _, p := range c.Publishers {
			if p.PublishedPort != 0 {
				ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", p.URL, p.PublishedPort, p.TargetPort, p.Protocol))
			}
		}
		fmt.Printf("%-28s %-8s %-9s %-10s %-4d %s\n", c.Name, c.Service, c.State, c.Health, c.ExitCode, strings.Join(ports, ","))
	}
	return nil
}

func inspect(ctx context.Context, cli *command.DockerCli, svc api.Compose, project string) error {
	cs, err := svc.Ps(ctx, project, api.PsOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		res, err := cli.Client().ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		ct := res.Container
		fmt.Printf("container %s id=%s\n", c.Name, ct.ID[:12])
		keys := make([]string, 0, len(ct.Config.Labels))
		for k := range ct.Config.Labels {
			if strings.HasPrefix(k, "com.docker.compose.") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  label %s=%s\n", k, ct.Config.Labels[k])
		}
		for _, m := range ct.Mounts {
			fmt.Printf("  mount type=%s name=%s src=%s dst=%s rw=%v mode=%q\n", m.Type, m.Name, m.Source, m.Destination, m.RW, m.Mode)
		}
		fmt.Printf("  selinux process=%q mount=%q securityopt=%v\n", ct.ProcessLabel, ct.MountLabel, ct.HostConfig.SecurityOpt)
		fmt.Printf("  capadd=%v capdrop=%v binds=%v\n", ct.HostConfig.CapAdd, ct.HostConfig.CapDrop, ct.HostConfig.Binds)
		if ct.State != nil && ct.State.Health != nil {
			h := ct.State.Health
			fmt.Printf("  health %s failing=%d\n", h.Status, h.FailingStreak)
			if n := len(h.Log); n > 0 {
				last := h.Log[n-1]
				fmt.Printf("  last probe exit=%d output=%q\n", last.ExitCode, strings.TrimSpace(last.Output))
			}
		}
	}
	return nil
}

func containerIDs(ctx context.Context, cli *command.DockerCli, project string) map[string]string {
	ids := map[string]string{}
	res, err := cli.Client().ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+project),
	})
	if err != nil {
		return ids
	}
	for _, c := range res.Items {
		ids[c.Labels[api.ServiceLabel]] = c.ID
	}
	return ids
}

func sameIDs(a, b map[string]string) int {
	n := 0
	for k, v := range a {
		if b[k] == v {
			n++
		}
	}
	return n
}

// leftovers lists what is still labeled with the project after down.
func leftovers(ctx context.Context, cli *command.DockerCli, project string) error {
	f := make(client.Filters).Add("label", api.ProjectLabel+"="+project)
	cs, err := cli.Client().ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}
	vs, err := cli.Client().VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		return err
	}
	ns, err := cli.Client().NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return err
	}
	var vnames, nnames []string
	for _, v := range vs.Items {
		vnames = append(vnames, v.Name)
	}
	for _, n := range ns.Items {
		nnames = append(nnames, n.Name)
	}
	fmt.Printf("== left with label %s=%s: containers=%d volumes=%v networks=%v\n", api.ProjectLabel, project, len(cs.Items), vnames, nnames)
	return nil
}

// describe prints an error with its chain of types and the SDK's classifiers.
func describe(err error) string {
	var chain []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		chain = append(chain, fmt.Sprintf("%T", e))
	}
	var tags []string
	if api.IsNotFoundError(err) {
		tags = append(tags, "api.IsNotFoundError")
	}
	if api.IsErrCanceled(err) {
		tags = append(tags, "api.IsErrCanceled")
	}
	if client.IsErrConnectionFailed(err) {
		tags = append(tags, "client.IsErrConnectionFailed")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		tags = append(tags, "context.DeadlineExceeded")
	}
	for name, is := range map[string]func(error) bool{
		"errdefs.NotFound": cerrdefs.IsNotFound, "errdefs.InvalidArgument": cerrdefs.IsInvalidArgument,
		"errdefs.Conflict": cerrdefs.IsConflict, "errdefs.Internal": cerrdefs.IsInternal,
		"errdefs.PermissionDenied": cerrdefs.IsPermissionDenied, "errdefs.Unauthorized": cerrdefs.IsUnauthorized,
		"errdefs.Unavailable": cerrdefs.IsUnavailable, "errdefs.Unknown": cerrdefs.IsUnknown,
	} {
		if is(err) {
			tags = append(tags, name)
		}
	}
	tags = append(tags, versionSpecificTags(err)...)
	return fmt.Sprintf("%v\n   chain=%v tags=%v", err, chain, tags)
}

type eventLog struct {
	mu   sync.Mutex
	w    io.Writer
	last map[string]string
}

func (e *eventLog) Start(_ context.Context, op string) { fmt.Fprintf(e.w, "  [event] start %s\n", op) }
func (e *eventLog) Done(op string, ok bool) {
	fmt.Fprintf(e.w, "  [event] done %s success=%v\n", op, ok)
}
func (e *eventLog) On(events ...api.Resource) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.last == nil {
		e.last = map[string]string{}
	}
	for _, r := range events {
		key := r.StatusText() + " " + r.Text + " " + r.Details
		if e.last[r.ID] == key { // pull progress repeats; print changes only
			continue
		}
		e.last[r.ID] = key
		fmt.Fprintf(e.w, "  [event] %-34s %-7s %s %s\n", r.ID, r.StatusText(), r.Text, r.Details)
	}
}

type logPrinter struct{ w io.Writer }

func (l logPrinter) Log(c, m string)    { fmt.Fprintf(l.w, "%s | %s\n", c, m) }
func (l logPrinter) Err(c, m string)    { fmt.Fprintf(l.w, "%s ! %s\n", c, m) }
func (l logPrinter) Status(c, m string) { fmt.Fprintf(l.w, "%s # %s\n", c, m) }
