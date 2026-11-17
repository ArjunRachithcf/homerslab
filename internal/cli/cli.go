// Package cli is the homeward command: one binary that is the manager, the agent, and the CLI that talks to the
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

	"github.com/ArjunRachithcf/homeward/internal/agent"
	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/ldaps"
)

// Defaults: where the packaged units put things.
const (
	DefaultAgentSocket   = "/run/homeward-agent/agent.sock"
	DefaultControlSocket = "/run/homeward/manager.sock"
	DefaultDB            = "/var/lib/homeward/manager.db"
	DefaultListen        = ":7020"
	DefaultSetupPort     = 7020
	DefaultAgentState    = "/var/lib/homeward-agent"
	DefaultGroup         = "homeward"
	// DefaultProjection is Home's projection, which the manager writes and Home reads (#21).
	DefaultProjection = "/run/homeward-projection/home.json"
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
  homeward agent      run the agent (as root, from homeward-agent.service)
  homeward manager    run the manager (unprivileged, from homeward-manager.service)
  homeward home       run Home, where people open their apps (unprivileged, from homeward-home.service)
  homeward setup link make a one-time link to first-time setup, and show it with its QR code
  homeward setup secret mesh|dns
                       give first-time setup the mesh's or the DNS provider's token, from this machine's terminal
                       (read from stdin, never shown)
  homeward setup join show, in this machine's terminal only, a key for your device and a one-time link to your first
                       passkey, as QR codes: first-time setup's second half
  homeward setup passkey
                       a one-time link to a second passkey, until first-time setup is done
  homeward dns token  replace the DNS provider's token, from this machine's console, and renew the certificate if it
                       is due
  homeward node       show this primary as its agent reports it
  homeward catalog    list the apps in the catalog
  homeward plan APP   make an install plan for an app, and show it in plain words
  homeward plan show ID
                       show an install plan by its id
  homeward mesh owner-password
                       show the owner's password of a NetBird Homeward set up, for its dashboard (as root)
  homeward firewall restore
                       load the firewall table the agent last applied, or a closed one on the first boot (as root,
                       from homeward-firewall-restore.service)

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
	case "agent backup-lock": // a backup target's lock run, in restic's unit; not for people
		return backup.LockMain(args[2:], os.Getenv("CREDENTIALS_DIRECTORY"), os.Stdin, env.Stdout, env.Stderr)
	case "agent migrate": // homeward-migrate.service's: a node installed as HomersLab takes Homeward's names (#250)
		err = agentMigrate(ctx, args[2:], env)
	case "agent check-storage": // an app's unit's own check before each start; not for people
		return agent.CheckStorageMain(args[2:], env.Stderr)
	case "edge ldaps": // the sign-in listener's LDAPS port, its unit's process (#204); not for people
		return ldaps.Main(ctx, args[2:], env.Stderr)
	case "plan show":
		err = showPlanByID(ctx, args[2:], env)
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
		case "home":
			err = runHome(ctx, args[1:], env)
		case "node":
			err = showNode(ctx, args[1:], env)
		case "catalog":
			err = showCatalog(ctx, args[1:], env)
		case "plan":
			err = makePlan(ctx, args[1:], env)
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
		fmt.Fprintf(env.Stderr, "homeward: %v\n", err)
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
	fs := flag.NewFlagSet("homeward "+name, flag.ContinueOnError)
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
