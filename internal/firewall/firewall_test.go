package firewall_test

import (
	"context"
	_ "embed"
	"net/netip"
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
	if got, unusable := firewall.RestoreScript(path, 7020); unusable != nil || got != applied {
		t.Errorf("the restore loads %q, %v; want what was applied", got, unusable)
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
}

// savedBefore129 is a table as the agent saved it before #129, rendered by that agent's code.
//
//go:embed testdata/saved-before-129.nft
var savedBefore129 []byte

func TestASavedTableThatCantBeUsedBootsClosed(t *testing.T) {
	testenv.Isolate(t)
	table := firewall.Render(firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}, Bridges: []string{"docker0"}})
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty (a power cut before the data reached the disk)": "",
		"cut short in a chain":                                 table[:len(table)/2],
		"cut short at a chain's end":                           table[:strings.LastIndex(table, "\t}\n")+3],
		"cut short by its last newline":                        strings.TrimSuffix(table, "\n"),
		"not HomersLab's table":                                "flush ruleset\n",
		"HomersLab's table after something else":               "flush ruleset\n" + table,
		"HomersLab's table and another after it":               table + "}\n" + "table inet other {\n}\n",
		"HomersLab's table and a stray brace":                  table + "}\n",
	} {
		path := filepath.Join(dir, "firewall.nft")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, unusable := firewall.RestoreScript(path, 7444)
		if got != firewall.Closed(7444) || unusable == nil {
			t.Errorf("%s: the restore loads (%v):\n%s", name, unusable, got)
		}
	}
	// A table an earlier agent saved (before #129) is whole: it is loaded as it is.
	// (Embedded: the firewall's test binary also runs on its own, as root in CI.)
	legacy := filepath.Join(dir, "legacy.nft")
	if err := os.WriteFile(legacy, savedBefore129, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, unusable := firewall.RestoreScript(legacy, 7444); unusable != nil || got != string(savedBefore129) {
		t.Errorf("a table an earlier agent saved: %v", unusable)
	}
	// Unreadable: a folder where the file should be.
	unreadable := filepath.Join(dir, "folder.nft")
	_ = os.Mkdir(unreadable, 0o700)
	if got, unusable := firewall.RestoreScript(unreadable, 7444); got != firewall.Closed(7444) || unusable == nil {
		t.Errorf("unreadable: the restore loads (%v):\n%s", unusable, got)
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

func TestPort53IsTheGatewaysOwnAndTheResolversOnlyThroughTheLAN(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}, Bridges: []string{"docker0"},
		Resolvers: []netip.Addr{netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("2001:db8:1::1")}}
	r := firewall.Render(s)
	chain := func(name string) string {
		_, after, _ := strings.Cut(r, "\tchain "+name+" {\n")
		body, _, _ := strings.Cut(after, "\t}\n")
		return body
	}
	local := "meta l4proto { tcp, udp } th dport 53 fib daddr . iif type local accept"
	if got := chain("apps_in"); !strings.HasPrefix(got, "\t\t"+local+"\n") || strings.Count(got, "dport 53") != 1 {
		t.Errorf("apps_in lets port 53 in beyond the bridge's own address:\n%s", got)
	}
	if got := chain("virtual_in"); got != "\t\tudp dport { 67, 547 } accept\n\t\t"+local+"\n\t\tmeta l4proto { tcp, udp } th dport 53 drop\n" {
		t.Errorf("virtual_in, want its own gateway's DNS and then port 53 dropped:\n%s", got)
	}
	out := chain("apps_out")
	for _, want := range []string{
		`oifname { "enp5s0" } ip daddr { 192.168.1.1 } meta l4proto { tcp, udp } th dport 53 accept`,
		`oifname { "enp5s0" } ip6 daddr { 2001:db8:1::1 } meta l4proto { tcp, udp } th dport 53 accept`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("apps_out lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "dport 53"); n != 2 {
		t.Errorf("apps_out has %d port-53 rules, want the two through the LAN:\n%s", n, out)
	}
	noLAN := s
	noLAN.LAN = nil
	if strings.Contains(firewall.Render(noLAN), "daddr { 192.168.1.1 }") {
		t.Error("with no LAN known, apps still reach a resolver, through any interface")
	}
}
