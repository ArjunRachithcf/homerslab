package firewall_test

import (
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestTheTableIsReplacedInOneTransactionAndChangesNoOtherTable(t *testing.T) {
	testenv.Isolate(t)
	r := firewall.Render(firewall.State{Mesh: []string{"wt0"}, WireGuard: []int{51820, 51820}, SetupPort: 7020})
	lines := strings.Split(strings.TrimSpace(r), "\n")
	if lines[1] != "table inet homerslab" || lines[2] != "delete table inet homerslab" || lines[3] != "table inet homerslab {" {
		t.Errorf("the script doesn't replace the table atomically:\n%s", r)
	}
	for _, l := range lines {
		if f := strings.Fields(l); len(f) >= 2 && (f[0] == "table" || f[0] == "delete" || f[0] == "flush" || f[0] == "add") && !strings.Contains(l, "inet homerslab") {
			t.Errorf("the script touches another table: %q", l)
		}
	}
	if strings.Count(r, "udp dport 51820 accept") != 1 {
		t.Errorf("the WireGuard port isn't open exactly once:\n%s", r)
	}
	if strings.Count(r, "policy drop;") != 2 {
		t.Errorf("input and forward don't both drop by default:\n%s", r)
	}
}

func TestOnlyAnOpenSetupListenerIsLetIn(t *testing.T) {
	testenv.Isolate(t)
	open := firewall.Render(firewall.State{Mesh: []string{"wt0"}, SetupPort: 7020})
	closed := firewall.Render(firewall.State{Mesh: []string{"wt0"}})
	if !strings.Contains(open, "tcp dport 7020 accept") {
		t.Errorf("the open setup listener isn't let in:\n%s", open)
	}
	if strings.Contains(closed, "tcp dport") {
		t.Errorf("with the setup listener closed, a TCP port is still open to the LAN:\n%s", closed)
	}
}
