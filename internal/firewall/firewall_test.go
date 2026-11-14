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
	// The table's own wildcard, for the bridges the agent names, is the one there is.
	r = strings.Replace(r, "\t\toifname \"hla*\" jump to_apps\n", "", 1)
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
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}, Uplinks4: []string{"enp5s0"}, Uplinks6: []string{"enp5s0"},
		Bridges: []string{"docker0"}, Resolvers: []netip.Addr{netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("2001:db8:1::1")}}
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
	noLAN.LAN, noLAN.Uplinks4, noLAN.Uplinks6 = nil, nil, nil
	if strings.Contains(firewall.Render(noLAN), "daddr { 192.168.1.1 }") {
		t.Error("with no LAN known, apps still reach a resolver, through any interface")
	}
}

// A VPN's tunnel with a default route (wg-quick's in table 51820, or a client's in main) is how apps reach the
// internet then, but it is never the LAN (#108): apps may leave through it, nothing is forwarded from it, and the
// setup listener stays the LAN's. The mesh and the engines' bridges are never a way out.
func TestAppsLeaveThroughAVPNsTunnelThatIsNotTheLAN(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"eth0"}, Uplinks4: []string{"eth0", "wg0", "wt0", "docker0", "bad name"},
		Bridges: []string{"docker0"}, Virtual: []string{"docker0", "virbr0", "wg0"}, SetupPort: 7020,
		Resolvers: []netip.Addr{netip.MustParseAddr("10.2.0.1")}}
	r := firewall.Render(s)
	chain := func(name string) string {
		_, after, _ := strings.Cut(r, "\tchain "+name+" {\n")
		body, _, _ := strings.Cut(after, "\t}\n")
		return body
	}
	for name, want := range map[string]string{
		"outbound": `meta nfproto ipv4 oifname != { "eth0", "wg0" } drop`,
		"forward":  `iifname { "eth0", "wg0" } drop`,
		"apps_out": `oifname { "eth0", "wg0" } ip daddr { 10.2.0.1 } meta l4proto { tcp, udp } th dport 53 accept`,
		"input":    `iifname { "eth0" } tcp dport 7020 accept`,
	} {
		if !strings.Contains(chain(name), want) {
			t.Errorf("%s lacks %q:\n%s", name, want, chain(name))
		}
	}
	if got := chain("input"); !strings.Contains(got, `iifname { "virbr0" } meta iifkind "bridge" jump virtual_in`) {
		t.Errorf("input treats the VPN as a local virtual network:\n%s", got)
	}
	noLAN := s
	noLAN.LAN, noLAN.Uplinks4 = nil, []string{"wg0"}
	if got := firewall.Render(noLAN); !strings.Contains(got, `meta nfproto ipv4 oifname != { "wg0" } drop`) || strings.Contains(got, "dport 7020") {
		t.Errorf("with only a VPN's default route, want apps out through it and no setup listener:\n%s", got)
	}
}

// A tunnel, a VPN's or any other, with a default route or without, is never a local virtual network (#148): it gets
// no DHCP and no DNS from this machine, only what every interface gets (the machine's basics), and nothing from it is
// forwarded, the internet included. Only a bridge, by the kernel's kind of link, is a local virtual network, an app
// network or forwarded at all: so a tunnel the agent hasn't seen yet, one an engine names a bridge, or an interface of
// no kind (a second NIC, which a match on a kind never matches) is none of them. The mesh stays the mesh.
func TestOnlyABridgeIsALocalVirtualNetworkOrForwarded(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"eth0"}, Uplinks4: []string{"eth0", "wg0"}, Uplinks6: []string{"eth0"},
		Bridges: []string{"docker0", "tun0"}, Virtual: []string{"docker0", "virbr0"}}
	r := firewall.Render(s)
	chain := func(name string) string {
		_, after, _ := strings.Cut(r, "\tchain "+name+" {\n")
		body, _, _ := strings.Cut(after, "\t}\n")
		return body
	}
	input := chain("input")
	for _, want := range []string{
		`iifname { "wt0" } accept`,
		`iifname { "docker0" } meta iifkind "bridge" jump apps_in`,
		`iifname { "virbr0" } meta iifkind "bridge" jump virtual_in`,
	} {
		if !strings.Contains(input, want) {
			t.Errorf("input lacks %q:\n%s", want, input)
		}
	}
	forward := chain("forward")
	want := "\t\tiifname { \"docker0\" } meta iifkind \"bridge\" jump apps_out\n" +
		"\t\tiifname { \"eth0\", \"wg0\" } drop\n" +
		"\t\tiifname { \"virbr0\" } meta iifkind \"bridge\" jump outbound\n"
	if !strings.HasSuffix(forward, want) || strings.Count(forward, "jump outbound") != 1 {
		t.Errorf("forward, want only bridges forwarded to the internet, by a match on their kind:\n%s", forward)
	}
	// With no LAN known (at boot, or a VPN that replaced the default route) no interface is a local virtual network,
	// and none but an engine's bridge is forwarded: the LAN may be a bridge itself.
	boot := firewall.Render(firewall.State{Mesh: []string{"wt0"}, Uplinks4: []string{"tun0"}, Bridges: []string{"docker0"},
		Virtual: []string{"docker0", "virbr0"}})
	if strings.Contains(boot, "jump virtual_in") || strings.Contains(boot, `meta iifkind "bridge" jump outbound`) ||
		!strings.Contains(boot, "\t\tiifname { \"docker0\" } meta iifkind \"bridge\" jump apps_out\n\t\tiifname { \"tun0\" } drop\n\t}") {
		t.Errorf("at boot:\n%s", boot)
	}
}

// A bridge with a NIC among its ports (a second NIC enslaved to it) reaches past this machine: it is no local virtual
// network and no app network (#169). Only the bridges the kernel's last reading found with guests' ports alone (State.
// Virtual) are: a bridge with a NIC, or one the agent hasn't read yet, gets no virtual_in and nothing from it is
// forwarded, as for that NIC; an engine's bridge with a NIC gets nothing in at all, and nothing forwarded. With no
// reading, no bridge is either.
func TestABridgeWithANICIsTreatedAsThatNIC(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"eth0"}, Uplinks4: []string{"eth0"}, Uplinks6: []string{"eth0"},
		Bridges: []string{"docker0", "br-nic"}, Virtual: []string{"docker0", "virbr0", "eth0", "wt0", "bad name"}}
	r := firewall.Render(s)
	chain := func(r, name string) string {
		_, after, _ := strings.Cut(r, "\tchain "+name+" {\n")
		body, _, _ := strings.Cut(after, "\t}\n")
		return body
	}
	input, forward := chain(r, "input"), chain(r, "forward")
	for _, want := range []string{
		`iifname { "docker0" } meta iifkind "bridge" jump apps_in`,
		`iifname { "br-nic" } meta iifkind "bridge" drop`,
		`iifname { "virbr0" } meta iifkind "bridge" jump virtual_in`,
	} {
		if !strings.Contains(input, want) {
			t.Errorf("input lacks %q:\n%s", want, input)
		}
	}
	for _, want := range []string{
		`iifname { "docker0" } meta iifkind "bridge" jump apps_out`,
		`iifname { "virbr0" } meta iifkind "bridge" jump outbound`,
	} {
		if !strings.Contains(forward, want) {
			t.Errorf("forward lacks %q:\n%s", want, forward)
		}
	}
	if strings.Count(r, "virtual_in") != 2 || strings.Count(forward, "jump") != 2 || strings.Contains(chain(r, "apps_out"), "br-nic") {
		t.Errorf("a bridge that isn't a local virtual network, the LAN or the mesh is one:\n%s", r)
	}
	unread := s
	unread.Virtual = nil
	r = firewall.Render(unread)
	if strings.Contains(chain(r, "input"), "jump virtual_in") || strings.Contains(chain(r, "forward"), "jump") ||
		!strings.Contains(chain(r, "input"), `iifname { "br-nic", "docker0" } meta iifkind "bridge" drop`) {
		t.Errorf("with no bridge read as a local virtual network, want none forwarded or let in:\n%s", r)
	}
}

// Each address family leaves only through its own uplinks (#169): a tunnel that is a way out for IPv6 only (its routes
// cover only the IPv6 internet) is none for IPv4, and the other way round; the LAN's interface is one for the families
// of its default routes. A family with no uplink has no way out, and the machine's resolvers are reached through that
// family's uplinks only. Nothing is forwarded from any of them.
func TestEachFamilyLeavesOnlyThroughItsOwnUplinks(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"eth0"}, Uplinks4: []string{"eth0", "v4only"},
		Uplinks6: []string{"eth0", "v6only"}, Bridges: []string{"docker0"},
		Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("2001:db8:53::53")}}
	chain := func(r, name string) string {
		_, after, _ := strings.Cut(r, "\tchain "+name+" {\n")
		body, _, _ := strings.Cut(after, "\t}\n")
		return body
	}
	r := firewall.Render(s)
	if got, want := chain(r, "outbound"), "\t\tmeta nfproto ipv4 oifname != { \"eth0\", \"v4only\" } drop\n"+
		"\t\tmeta nfproto ipv6 oifname != { \"eth0\", \"v6only\" } drop\n"; !strings.HasPrefix(got, want) {
		t.Errorf("outbound, want each family through its own uplinks:\n%s", got)
	}
	for _, want := range []string{
		`oifname { "eth0", "v4only" } ip daddr { 192.0.2.53 } meta l4proto { tcp, udp } th dport 53 accept`,
		`oifname { "eth0", "v6only" } ip6 daddr { 2001:db8:53::53 } meta l4proto { tcp, udp } th dport 53 accept`,
	} {
		if !strings.Contains(chain(r, "apps_out"), want) {
			t.Errorf("apps_out lacks %q:\n%s", want, chain(r, "apps_out"))
		}
	}
	if !strings.Contains(chain(r, "forward"), `iifname { "eth0", "v4only", "v6only" } drop`) {
		t.Errorf("forward, want nothing forwarded from any uplink:\n%s", chain(r, "forward"))
	}
	// With no IPv6 default route (an ISP without IPv6, a router announcing a ULA and its resolver): no way out over
	// IPv6, but the machine's IPv6 resolver stays reachable on the LAN, as it was (#169's review). Never through a
	// tunnel that is no IPv6 uplink.
	v4only := s
	v4only.Uplinks6 = nil
	r = firewall.Render(v4only)
	if got := chain(r, "outbound"); !strings.HasPrefix(got, "\t\tmeta nfproto ipv4 oifname != { \"eth0\", \"v4only\" } drop\n"+
		"\t\tmeta nfproto ipv6 drop\n") {
		t.Errorf("with no IPv6 uplink, want no way out over IPv6:\n%s", got)
	}
	if got := chain(r, "apps_out"); !strings.Contains(got, `oifname { "eth0" } ip6 daddr { 2001:db8:53::53 } meta l4proto { tcp, udp } th dport 53 accept`) {
		t.Errorf("with no IPv6 uplink, want the IPv6 resolver on the LAN only:\n%s", got)
	}
	v6only := s
	v6only.Uplinks4 = nil
	if got := chain(firewall.Render(v6only), "apps_out"); !strings.Contains(got, `oifname { "eth0" } ip daddr { 192.0.2.53 } meta l4proto { tcp, udp } th dport 53 accept`) {
		t.Errorf("with no IPv4 uplink, want the IPv4 resolver on the LAN only:\n%s", got)
	}
	none := s
	none.Uplinks4, none.Uplinks6 = nil, nil
	if got := chain(firewall.Render(none), "outbound"); !strings.HasPrefix(got, "\t\tdrop\n") {
		t.Errorf("with no uplink, want no way out (the LAN is one only as its default routes say):\n%s", got)
	}
}

// Only the edge reaches an app (ADR 0020, #82): a process of this machine opens a new connection into an app's
// network only to an upstream, and only as a user the upstream names (the edge's, the agent's for its probes, the
// manager's for the sign-in). Everything else into the apps' networks is dropped: another app's port, another of the
// app's containers, any other user.
func TestOnlyTheUpstreamsUsersOpenConnectionsIntoAppNetworks(t *testing.T) {
	testenv.Isolate(t)
	s := firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}, Bridges: []string{"br-app", "br-signin"},
		Virtual: []string{"br-app", "br-signin"},
		Upstreams: []firewall.Upstream{
			{Addr: netip.MustParseAddrPort("172.31.240.2:8080"), UIDs: []uint32{981, 0}},
			{Addr: netip.MustParseAddrPort("172.31.250.10:9000"), UIDs: []uint32{981, 980, 0, 980}},
		}}
	r := firewall.Render(s)
	for _, want := range []string{
		"\tchain output {\n\t\ttype filter hook output priority filter - 5; policy accept;\n\t\toifname \"hla*\" jump to_apps\n",
		`oifname { "br-app", "br-signin" } jump to_apps`,
		"ip daddr . meta l4proto . th dport { 172.31.240.2 . tcp . 8080, 172.31.250.10 . tcp . 9000 } jump to_apps",
		"\tchain to_apps {\n\t\tct state established,related accept\n\t\tmeta l4proto { icmp, ipv6-icmp } accept\n" +
			"\t\tip daddr . tcp dport . meta skuid { 172.31.240.2 . 8080 . 0, 172.31.240.2 . 8080 . 981, " +
			"172.31.250.10 . 9000 . 0, 172.31.250.10 . 9000 . 980, 172.31.250.10 . 9000 . 981 } accept\n\t\tdrop\n\t}\n",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("the table lacks %q:\n%s", want, r)
		}
	}
	// No upstream: nothing of this machine opens a connection into an app's network at all; and an app's bridge the
	// engines haven't listed yet is kept to the edge by its name.
	none := firewall.Render(firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}, Bridges: []string{"br-app"}, Virtual: []string{"br-app"}})
	if !strings.Contains(none, "\t\toifname \"hla*\" jump to_apps\n") || !strings.Contains(none, "\tchain to_apps {\n\t\tct state established,related accept\n\t\tmeta l4proto { icmp, ipv6-icmp } accept\n\t\tdrop\n\t}\n") {
		t.Errorf("without an upstream:\n%s", none)
	}
	// An upstream that isn't IPv4 with a port, or no user: never rendered.
	odd := firewall.Render(firewall.State{Upstreams: []firewall.Upstream{{Addr: netip.MustParseAddrPort("[fd00::2]:80"), UIDs: []uint32{0}},
		{Addr: netip.MustParseAddrPort("172.31.240.2:0"), UIDs: []uint32{0}}, {Addr: netip.MustParseAddrPort("172.31.240.3:80")}}})
	if strings.Contains(odd, "fd00::2") || strings.Contains(odd, ". 0 ") || strings.Contains(odd, "172.31.240.3") {
		t.Errorf("an odd upstream was rendered:\n%s", odd)
	}
	if !s.Equal(s) || s.Equal(firewall.State{Mesh: s.Mesh, LAN: s.LAN, Bridges: s.Bridges, Virtual: s.Virtual}) {
		t.Error("the upstreams don't count in Equal")
	}
}
