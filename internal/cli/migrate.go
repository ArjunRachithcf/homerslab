package cli

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/ArjunRachithcf/homeward/internal/host"
	"github.com/ArjunRachithcf/homeward/internal/migrate"
)

// LegacyAgentState is the agent's state folder before #250, which `homeward firewall restore` reads (and never
// writes) until homeward-migrate.service has moved it: for one release.
const LegacyAgentState = "/" + migrate.OldAgentState

// errNotMigrated is a node installed as HomersLab whose folders haven't moved yet: the agent and the manager would
// start without their state, as a new node, with first-time setup open.
var errNotMigrated = errors.New("this node was installed as HomersLab and hasn't taken Homeward's names yet: " +
	"systemctl start homeward-migrate.service, and see journalctl -u homeward-migrate")

// agentMigrate is `homeward agent migrate`, homeward-migrate.service's: it moves a node installed as HomersLab to
// Homeward's names (#250), and does nothing on one that has none of HomersLab's left.
func agentMigrate(ctx context.Context, args []string, env Env) error {
	fs := flags("agent migrate", env)
	if err := parse(fs, args); err != nil {
		return err
	}
	if env.Euid() != 0 {
		return errors.New("moving the node to Homeward's names needs root")
	}
	_, err := migrate.Run(ctx, migrate.Host{Root: "/", Run: host.Exec, Log: slog.New(slog.NewTextHandler(env.Stderr, nil))})
	return err
}

// pending is the check the agent and the manager make before they start on their packaged folders; tests replace it.
var pending = func() bool { return migrate.Pending("/") }

// restoreFrom is the folder `homeward firewall restore` reads the table from: the agent's, or, until
// homeward-migrate.service has moved it (a boot it didn't run before, an upgrade not yet rebooted), the same folder
// under its name before #250, read only.
func restoreFrom(state, legacy string) string {
	if _, err := os.Stat(state); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return state
}
