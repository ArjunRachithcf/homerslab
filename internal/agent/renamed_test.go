package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/agent"
	"github.com/ArjunRachithcf/homeward/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homeward/internal/migrate"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// beforeRename is an agent's state as HomersLab's agent saved it, as homeward-migrate.service moved it: an app on a
// share (whose mount names the share's unit and mount point by their old names), the sign-in with its LDAP outpost,
// and the edge with a certificate.
const beforeRename = `{"enforcing":false,
 "edge":{"domains":["example.test"]},
 "signIn":{"provider":"authentik","bootstrapping":false,"ldap":true},
 "storage":{"media":{"kind":"share","protocol":"nfs","server":"100.64.1.2","share":"/media"}},
 "apps":{"ledger":{"product":"ledger","plan":"p-1","name":"Ledger","service":"web","upstream":"172.31.240.2:8080",
  "subnet":"172.31.240.0/28","level":"s0:c600,c601","secrets":[],"health":[],
  "project":{"name":"homerslab-ledger","services":[{"name":"web","image":"example/ledger:1"}]},
  "mounts":[{"location":"media","service":"web","source":"/var/lib/homerslab-storage/media","target":"/media","unit":"homerslab-storage-media.service"}]}}}`

// renamedNode is an agent starting on a state migrate.Run moved, with its mark.
func renamedNode(t *testing.T, h *agenttest.Host) string {
	t.Helper()
	state := t.TempDir()
	for name, b := range map[string]string{agent.StateFile: beforeRename, migrate.Mark: "homerslab\n"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agenttest.StartWith(t, agent.Config{StateDir: state, Run: h.Run, Root: h.Root, CheckEvery: 50 * time.Millisecond})
	return state
}

// On a node that was HomersLab's, the agent writes what it derives from its state again, under Homeward's names: the
// share's unit, the app's compose file and unit (its mount under the share's new names), the edge's, the sign-in's
// and the LDAP outpost's drop-ins; systemd enables them all and restarts them, and the mark goes.
func TestAMigratedNodesUnitsAreWrittenAgainUnderHomewardsNames(t *testing.T) {
	testenv.Isolate(t)
	h := agenttest.NewHost(t.TempDir())
	state := renamedNode(t, h)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(state, migrate.Mark)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mark is still there")
		}
		time.Sleep(20 * time.Millisecond)
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(h.Root, p))
		if err != nil {
			t.Errorf("/%s: %v", p, err)
		}
		return string(b)
	}
	for p, want := range map[string]string{
		"etc/homeward/units/homeward-storage-media.service":                            "/var/lib/homeward-storage/media",
		"etc/homeward/units/homeward-app-ledger.service":                               "homeward-storage-media.service",
		"etc/systemd/system/homeward-edge.service.d/50-homeward-certificates.conf":     filepath.Join(state, "edge"),
		"etc/systemd/system/homeward-authentik.service.d/50-homeward-credentials.conf": filepath.Join(state, "sign-in"),
		"etc/systemd/system/homeward-ldap.service.d/50-homeward-credentials.conf":      "ldap-token",
	} {
		if got := read(p); !strings.Contains(got, want) {
			t.Errorf("/%s doesn't say %q:\n%s", p, want, got)
		}
	}
	if b, err := os.ReadFile(filepath.Join(state, "apps", "ledger", "homeward.yaml")); err != nil || !strings.Contains(string(b), `source: "/var/lib/homeward-storage/media"`) {
		t.Errorf("the app's compose file of the agent's:\n%s %v", b, err)
	}
	enable := h.Ran("systemctl enable /etc/homeward/units/homeward-storage-media.service homeward-edge.service homeward-authentik.service homeward-ldap.service /etc/homeward/units/homeward-app-ledger.service")
	restart := h.Ran("systemctl --no-block restart homeward-storage-media.service homeward-edge.service homeward-authentik.service homeward-ldap.service homeward-app-ledger.service")
	if enable < 0 || restart < enable {
		t.Errorf("the units weren't enabled, then restarted: %q", h.Cmds())
	}
	for _, c := range h.Cmds() {
		if strings.Contains(c.String(), "homerslab-storage") || strings.Contains(c.String(), "homerslab-app") {
			t.Errorf("an old name was used: %s", c)
		}
	}
}

// A step that fails leaves the mark, and a later check (or the agent's next start) does it all again.
func TestAMigratedNodeWhoseUnitsCantBeEnabledTriesAgain(t *testing.T) {
	testenv.Isolate(t)
	h := agenttest.NewHost(t.TempDir())
	h.Fail("systemctl enable")
	state := renamedNode(t, h)
	waitChecks()
	if _, err := os.Stat(filepath.Join(state, migrate.Mark)); err != nil {
		t.Fatalf("the mark went though systemd enabled nothing: %v", err)
	}
	h.Mend()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(state, migrate.Mark)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mark stayed once systemd worked again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.Times("systemctl --no-block restart homeward-storage-media.service homeward-edge.service homeward-authentik.service homeward-ldap.service homeward-app-ledger.service") != 1 {
		t.Errorf("the units restarted %d times, want once", h.Times("systemctl --no-block restart homeward-storage-media.service"))
	}
}
