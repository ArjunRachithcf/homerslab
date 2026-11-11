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
)

// Defaults: where the packaged units put things.
const (
	DefaultAgentSocket   = "/run/homerslab-agent/agent.sock"
	DefaultControlSocket = "/run/homerslab/manager.sock"
	DefaultDB            = "/var/lib/homerslab/manager.db"
	DefaultListen        = ":7020"
	DefaultSetupPort     = 7020
	DefaultAgentState    = "/var/lib/homerslab-agent"
	DefaultGroup         = "homerslab"
)

// Env is what a command reaches of the world.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Euid           func() int
}

// OS is the real environment.
func OS() Env { return Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Euid: os.Geteuid} }

const usage = `Usage:
  homerslab agent      run the agent (as root, from homerslab-agent.service)
  homerslab manager    run the manager (unprivileged, from homerslab-manager.service)
  homerslab setup link make a one-time link to first-time setup, and show it with its QR code
  homerslab setup secret mesh|dns
                       give first-time setup the mesh's or the DNS provider's token, from this machine's terminal
                       (read from stdin, never shown)
  homerslab setup join show, in this machine's terminal only, a key for your device and a one-time link to your first
                       passkey, as QR codes: first-time setup's second half
  homerslab setup passkey
                       a one-time link to a second passkey, until first-time setup is done
  homerslab dns token  replace the DNS provider's token, from this machine's console, and renew the certificate if it
                       is due
  homerslab node       show this primary as its agent reports it
  homerslab mesh owner-password
                       show the owner's password of a NetBird HomersLab set up, for its dashboard (as root)
  homerslab firewall restore
                       load the firewall table the agent last applied, or a closed one on the first boot (as root,
                       from homerslab-firewall-restore.service)

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
	case "setup secret":
		err = setupSecret(ctx, args[2:], env)
	case "setup join":
		err = setupJoin(ctx, args[2:], env)
	case "setup passkey":
		err = setupPasskey(ctx, args[2:], env)
	case "dns token":
		err = dnsToken(ctx, args[2:], env)
	case "firewall restore":
		err = firewallRestore(ctx, args[2:], env)
	case "mesh owner-password":
		err = meshOwnerPassword(ctx, args[2:], env)
	default:
		switch strings.Join(first(args, 1), " ") {
		case "agent":
			err = runAgent(ctx, args[1:], env)
		case "manager":
			err = runManager(ctx, args[1:], env)
		case "node":
			err = showNode(ctx, args[1:], env)
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
