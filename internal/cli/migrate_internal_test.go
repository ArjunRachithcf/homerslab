package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// On a node installed as HomersLab whose folders haven't moved yet, the agent and the manager refuse to start on
// their packaged folders: they'd start without their state, as a new node, with first-time setup open (#250).
func TestTheAgentAndTheManagerRefuseANodeNotYetMigrated(t *testing.T) {
	testenv.Isolate(t)
	defer func(was func() bool) { pending = was }(pending)
	pending = func() bool { return true }
	for _, args := range [][]string{{"agent"}, {"manager"}} {
		var stderr bytes.Buffer
		code := Main(context.Background(), args, Env{Stdout: &bytes.Buffer{}, Stderr: &stderr, Euid: func() int { return 1000 }})
		if code != 1 || !strings.Contains(stderr.String(), "systemctl start homeward-migrate.service") {
			t.Errorf("homeward %s: %d, %q; want it refused, naming the migration's unit", args[0], code, stderr.String())
		}
	}
}

// `homeward firewall restore` reads the table from the agent's folder, or, while that is missing and the folder's
// name before #250 is there, from that one: never the closed table on a node whose migration hasn't run.
func TestTheFirewallTableIsReadFromTheOldFolderUntilItMoves(t *testing.T) {
	testenv.Isolate(t)
	dir := t.TempDir()
	state, legacy := filepath.Join(dir, "homeward-agent"), filepath.Join(dir, "homerslab-agent")
	if got := restoreFrom(state, legacy); got != state {
		t.Errorf("neither there: %s, want the agent's", got)
	}
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := restoreFrom(state, legacy); got != legacy {
		t.Errorf("only the old one there: %s, want it", got)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := restoreFrom(state, legacy); got != state {
		t.Errorf("both there: %s, want the agent's", got)
	}
}

// The migration is root's.
func TestTheMigrationNeedsRoot(t *testing.T) {
	testenv.Isolate(t)
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"agent", "migrate"}, Env{Stdout: &bytes.Buffer{}, Stderr: &stderr, Euid: func() int { return 1000 }})
	if code != 1 || !strings.Contains(stderr.String(), "needs root") {
		t.Errorf("%d, %q", code, stderr.String())
	}
}
