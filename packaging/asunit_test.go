package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// CI runs the agent's tests with the agent unit's capabilities, which .github/scripts/as-unit.sh reads from the unit
// file. It models only CapabilityBoundingSet= and NoNewPrivileges= of a root service, so it refuses a unit it would
// misread rather than drift from it: a drop-in beside it, an indented or continued line, a User=, Group= or
// DynamicUser=, or AmbientCapabilities= (#183). It checks the unit before it asks for root.
func TestAsUnitRefusesAUnitItWouldMisread(t *testing.T) {
	testenv.Isolate(t)
	real, err := os.ReadFile("systemd/homerslab-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../.github/scripts/as-unit.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, unit string) (int, string) {
		t.Helper()
		out, err := exec.Command("sh", script, unit, "true").CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, string(out)
	}
	unit := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "homerslab-agent.service")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if os.Geteuid() != 0 {
		if code, out := run(t, unit(t, string(real))); code != 2 || !strings.Contains(out, "run as root") {
			t.Errorf("the agent's unit: %d %s, want it read, then root asked for", code, out)
		}
	}
	for name, c := range map[string]struct{ body, dropIn, says string }{
		"an indented line":    {strings.Replace(string(real), "\nNoNewPrivileges=yes\n", "\n  NoNewPrivileges=no\nNoNewPrivileges=yes\n", 1), "", "indented lines in"},
		"a continued line":    {strings.Replace(string(real), "\nCapabilityBoundingSet=CAP_CHOWN ", "\nCapabilityBoundingSet=CAP_CHOWN \\\nCAP_KILL ", 1), "", "continued lines in"},
		"User=":               {strings.Replace(string(real), "\n[Service]\n", "\n[Service]\nUser=homerslab\n", 1), "", ":User=homerslab\n"},
		"Group=":              {strings.Replace(string(real), "\n[Service]\n", "\n[Service]\nGroup=homerslab\n", 1), "", ":Group=homerslab\n"},
		"DynamicUser=":        {strings.Replace(string(real), "\n[Service]\n", "\n[Service]\nDynamicUser=yes\n", 1), "", ":DynamicUser=yes\n"},
		"AmbientCapabilities": {strings.Replace(string(real), "\n[Service]\n", "\n[Service]\nAmbientCapabilities=CAP_KILL\n", 1), "", ":AmbientCapabilities=CAP_KILL\n"},
		"its drop-in":         {string(real), "homerslab-agent.service.d", "drop-in folder"},
		"a prefix's drop-in":  {string(real), "homerslab-.service.d", "drop-in folder"},
		"every service's":     {string(real), "service.d", "drop-in folder"},
	} {
		t.Run(name, func(t *testing.T) {
			p := unit(t, c.body)
			if c.dropIn != "" {
				if err := os.Mkdir(filepath.Join(filepath.Dir(p), c.dropIn), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if code, out := run(t, p); code != 2 || !strings.Contains(out, c.says) {
				t.Errorf("%d %s, want it refused for %s", code, out, c.says)
			}
		})
	}
}
