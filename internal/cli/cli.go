// Package cli is the homerslab command: one binary that is the manager, the agent, and the CLI that talks to the
// manager's API.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
)

// Defaults: where the packaged units put things.
const (
	DefaultAgentSocket   = "/run/homerslab-agent/agent.sock"
	DefaultControlSocket = "/run/homerslab/manager.sock"
	DefaultDB            = "/var/lib/homerslab/manager.db"
	DefaultListen        = ":7020"
	DefaultAdminListen   = "127.0.0.1:7021"
	DefaultGroup         = "homerslab"
)

// Env is what a command reaches of the world.
type Env struct {
	Stdout, Stderr io.Writer
	Euid           func() int
}

// OS is the real environment.
func OS() Env { return Env{Stdout: os.Stdout, Stderr: os.Stderr, Euid: os.Geteuid} }

const usage = `Usage:
  homerslab agent      run the agent (as root, from homerslab-agent.service)
  homerslab manager    run the manager (unprivileged, from homerslab-manager.service)
  homerslab setup link make a one-time link to first-time setup, and show it with its QR code
  homerslab node       show this primary as its agent reports it
  homerslab catalog    list the apps in the catalog
  homerslab plan APP   make an install plan for an app, and show it in plain words
  homerslab plan show ID
                       show an install plan by its id
  homerslab admin      say where Admin opens, on this machine

Each command takes --help.
`

// errUsage is a mistake in the command line; flag has already said what it was.
var errUsage = errors.New("usage")

// Main runs a command and returns its exit code.
func Main(ctx context.Context, args []string, env Env) int {
	var err error
	switch strings.Join(first(args, 2), " ") {
	case "setup link":
		err = setupLink(ctx, args[2:], env)
	case "agent read-project": // the agent's own child for one compose read; not for people
		return agent.ReadProjectMain(args[2:], os.Stdin, env.Stdout)
	case "plan show":
		err = showPlanByID(ctx, args[2:], env)
	default:
		switch strings.Join(first(args, 1), " ") {
		case "agent":
			err = runAgent(ctx, args[1:], env)
		case "manager":
			err = runManager(ctx, args[1:], env)
		case "node":
			err = showNode(ctx, args[1:], env)
		case "catalog":
			err = showCatalog(ctx, args[1:], env)
		case "plan":
			err = makePlan(ctx, args[1:], env)
		case "admin":
			err = showAdmin(ctx, args[1:], env)
		default:
			fmt.Fprint(env.Stderr, usage)
			return 2
		}
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		fmt.Fprintf(env.Stderr, "homerslab: %v\n", err)
		return 1
	}
}

func first(args []string, n int) []string {
	if len(args) < n {
		return nil
	}
	return args[:n]
}

// flags is a command's flag set, writing its errors to stderr.
func flags(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet("homerslab "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "unexpected %q\n", fs.Arg(0))
		fs.Usage()
		return errUsage
	}
	return nil
}
