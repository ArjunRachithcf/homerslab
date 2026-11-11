// Package compose is the container-engine adapter's use of Docker Compose's SDK (github.com/docker/compose/v5), pinned
// exactly in go.mod (ADR 0014). It is the only package that imports the SDK, and this file the only one that makes its
// service, so the SDK's traps are guarded in one place (Compose SDK spike).
package compose

import (
	"errors"
	"io"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
)

// ErrDryRunWithStreams refuses the one combination of options that the SDK turns from a preview into a real run: in
// v5.5–v5.7, with an output or error stream set, NewComposeService wraps the original client and drops the dry-run one.
var ErrDryRunWithStreams = errors.New("a dry run with an output or error stream would apply its changes for real")

// NoEngine is an engine endpoint where nothing listens: for a reader, which needs no engine and must reach none.
const NoEngine = "unix:///nonexistent"

// options are the SDK service options this adapter uses.
type options struct {
	dryRun   bool
	out, err io.Writer
}

// newCLI is the Docker CLI object the SDK needs, pointed at one engine endpoint (the environment's when host is "").
// Making it reaches no engine.
func newCLI(host string) (*command.DockerCli, error) {
	cli, err := command.NewDockerCli()
	if err != nil {
		return nil, err
	}
	o := flags.NewClientOptions()
	if host != "" {
		o.Hosts = []string{host}
	}
	if err := cli.Initialize(o); err != nil {
		return nil, err
	}
	return cli, nil
}

// newService makes the SDK's compose service. It never pairs a dry run with a stream, and it never prompts: any
// question the SDK would ask is answered no.
func newService(cli command.Cli, o options) (api.Compose, error) {
	if o.dryRun && (o.out != nil || o.err != nil) {
		return nil, ErrDryRunWithStreams
	}
	opts := []compose.Option{compose.WithPrompt(func(string, bool) (bool, error) { return false, nil })}
	if o.dryRun {
		opts = append(opts, compose.WithDryRun)
	}
	if o.out != nil {
		opts = append(opts, compose.WithOutputStream(o.out))
	}
	if o.err != nil {
		opts = append(opts, compose.WithErrorStream(o.err))
	}
	return compose.NewComposeService(cli, opts...)
}
