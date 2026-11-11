package firewall_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
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
	lan := []string{"enp5s0"}
	open := firewall.Render(firewall.State{Mesh: []string{"wt0"}, SetupPort: 7020, LAN: lan})
	closed := firewall.Render(firewall.State{Mesh: []string{"wt0"}, LAN: lan})
	noLAN := firewall.Render(firewall.State{Mesh: []string{"wt0"}, SetupPort: 7020})
	if !strings.Contains(open, `iifname { "enp5s0" } tcp dport 7020 accept`) {
		t.Errorf("the open setup listener isn't let in:\n%s", open)
	}
	if strings.Contains(closed, "tcp dport") {
		t.Errorf("with the setup listener closed, a TCP port is still open to the LAN:\n%s", closed)
	}
	if strings.Contains(noLAN, "tcp dport") {
		t.Errorf("with no LAN known, the setup listener is open to every interface:\n%s", noLAN)
	}
}

func TestTheTableAppliedIsSavedForTheNextBoot(t *testing.T) {
	testenv.Isolate(t)
	path := filepath.Join(t.TempDir(), "state", "firewall.nft")
	var applied string
	fw := &firewall.Firewall{SavePath: path, Run: func(_ context.Context, c host.Cmd) ([]byte, error) {
		if c.String() == "nft -f -" {
			applied = string(c.Stdin)
		}
		return []byte(applied), nil
	}}
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}}
	if err := fw.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != applied || applied != firewall.Render(s) {
		t.Fatalf("saved %q, %v; applied %q", b, err, applied)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("saved with mode %o, want 600", fi.Mode().Perm())
	}
	if got, err := firewall.RestoreScript(path, 7020); err != nil || got != applied {
		t.Errorf("the restore loads %q, %v; want what was applied", got, err)
	}
}

func TestAMachineWithNoSavedTableBootsClosed(t *testing.T) {
	testenv.Isolate(t)
	got, err := firewall.RestoreScript(filepath.Join(t.TempDir(), "none.nft"), 7020)
	if err != nil || got != firewall.Closed(7020) {
		t.Fatalf("%q, %v", got, err)
	}
	if strings.Count(got, "policy drop;") != 2 || !strings.Contains(got, "tcp dport 7020 accept") || strings.Count(got, "accept") != 6 {
		t.Errorf("the closed table:\n%s", got)
	}
	bad := filepath.Join(t.TempDir(), "bad.nft")
	_ = os.WriteFile(bad, []byte("flush ruleset\n"), 0o600)
	if _, err := firewall.RestoreScript(bad, 7020); err == nil {
		t.Error("a file that isn't HomersLab's table was loaded")
	}
}

func TestNoNameNftWouldMisreadIsRendered(t *testing.T) {
	testenv.Isolate(t)
	r := firewall.Render(firewall.State{Mesh: []string{"wt0", "w*"}, LAN: []string{"enp5s0", `e"`},
		Bridges: []string{"br-ok", "br-*", "l*", "*", `x"y`, "a b", "sixteen-chars-xx", "docker0"}})
	for _, bad := range []string{"*", `x\"y`, `e\"`, "a b", "sixteen-chars-xx"} {
		if strings.Contains(r, bad) {
			t.Errorf("rendered %q:\n%s", bad, r)
		}
	}
	if !strings.Contains(r, `"br-ok"`) || !strings.Contains(r, `"docker0"`) {
		t.Errorf("a valid name went missing:\n%s", r)
	}
}
