package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/host"
	"github.com/ArjunRachithcf/homeward/internal/migrate"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// node is a machine's root under test, and the systemd and shadow tools its runs reach.
type node struct {
	t     *testing.T
	root  string
	units map[string][2]string // loaded units: name → load, active state
	ran   []string
}

// oldNode is a node as HomersLab 0.x installed it, with an app on a share, the sign-in and the edge: what the package
// made (users, units' links), what systemd and tmpfiles made for the new package before any run (its empty folders),
// and what the agent wrote.
func oldNode(t *testing.T) *node {
	t.Helper()
	n := &node{t: t, root: t.TempDir(), units: map[string][2]string{
		"homerslab-agent.service":            {"loaded", "active"},
		"homerslab-manager.service":          {"loaded", "active"},
		"homerslab-admin.socket":             {"loaded", "active"},
		"homerslab-edge.service":             {"loaded", "active"},
		"homerslab-authentik.service":        {"loaded", "active"},
		"homerslab-firewall-restore.service": {"loaded", "active"},
		"homerslab-app-ledger.service":       {"loaded", "active"},
		"homerslab-storage-media.service":    {"loaded", "active"},
		"homerslab-home.service":             {"loaded", "inactive"}, // socket-activated, not running: still stopped
		"homerslab-gone.service":             {"not-found", "inactive"},
	}}
	n.write("etc/passwd", "root:x:0:0:root:/root:/bin/bash\n"+
		"homerslab:x:60600:60600:HomersLab manager:/var/lib/homerslab:/usr/sbin/nologin\n"+
		"homerslab-edge:x:60601:60601:HomersLab edge:/var/lib/homerslab-edge:/usr/sbin/nologin\n"+
		"homerslab-read:x:60602:60602:HomersLab compose reads:/:/usr/sbin/nologin\n"+
		"homerslab-home:x:60603:60603:HomersLab Home:/:/usr/sbin/nologin\n"+
		"homerslab-ldaps:x:60604:60604:HomersLab LDAPS:/:/usr/sbin/nologin\n"+
		"maria:x:1000:1000::/home/maria:/bin/bash\n")
	n.write("etc/group", "root:x:0:\nwheel:x:10:maria\n"+
		"homerslab:x:60600:\nhomerslab-edge:x:60601:homerslab\nhomerslab-read:x:60602:\nhomerslab-home:x:60603:\n"+
		"homerslab-ldaps:x:60604:\nmaria:x:1000:\n")
	// The agent's state, with its sealed credentials.
	n.write("var/lib/homerslab-agent/agent.json", `{"enforcing":true}`)
	n.write("var/lib/homerslab-agent/firewall.nft", "table inet homerslab {}\n")
	n.write("var/lib/homerslab-agent/credentials/manager-home-client-secret.cred", "sealed: the client's secret")
	n.write("var/lib/homerslab-agent/edge/cert-example.test.cred", "sealed: a certificate")
	n.write("var/lib/homerslab-agent/apps/ledger/compose.yaml", "services: {web: {image: x}}\n")
	n.write("var/lib/homerslab-agent/apps/ledger/homerslab.yaml", "# the agent's\n")
	n.write("var/lib/homerslab-agent/apps/ledger/credentials/db-password.cred", "sealed: the app's")
	n.write("var/lib/homerslab/manager.db", "the household")
	n.write("var/lib/homerslab-edge/caddy/locks/x", "")
	n.mkdir("var/lib/homerslab-storage/media")
	n.write("var/cache/homerslab-agent/homerslab", "the agent, exec-only")
	n.write("etc/homerslab/units/homerslab-app-ledger.service", "[Unit]\n")
	n.write("etc/homerslab/units/homerslab-storage-media.service", "[Unit]\n")
	n.write("etc/systemd/system/homerslab-edge.service.d/50-homerslab-certificates.conf", "[Service]\n")
	n.write("etc/systemd/system/homerslab-authentik.service.d/50-homerslab-credentials.conf", "[Service]\n")
	n.write("etc/systemd/system/netbird.service.d/10-homerslab.conf", "[Service]\nEnvironment=NB_DISABLE_NAT_MAPPER=true\n")
	n.write("etc/systemd/system/netbird.service.d/11-homerslab-socket.conf", "[Service]\nRuntimeDirectory=netbird\n")
	n.write("etc/systemd/system/netbird.service.d/20-own.conf", "# someone's own\n")
	n.link("etc/systemd/system/homerslab-app-ledger.service", "/etc/homerslab/units/homerslab-app-ledger.service")
	n.link("etc/systemd/system/multi-user.target.wants/homerslab-app-ledger.service", "/etc/homerslab/units/homerslab-app-ledger.service")
	n.link("etc/systemd/system/multi-user.target.wants/homerslab-manager.service", "/usr/lib/systemd/system/homerslab-manager.service")
	n.link("etc/systemd/system/sockets.target.wants/homerslab-admin.socket", "/usr/lib/systemd/system/homerslab-admin.socket")
	n.link("etc/systemd/system/sysinit.target.wants/homerslab-firewall-restore.service", "/usr/lib/systemd/system/homerslab-firewall-restore.service")
	n.link("etc/systemd/system/multi-user.target.wants/sshd.service", "/usr/lib/systemd/system/sshd.service")
	// What the new package's systemd and tmpfiles made before the run.
	n.mkdir("var/lib/homeward-agent")
	n.mkdir("etc/homeward/units")
	return n
}

// reboot is the next boot, without the old package: its units' files are gone, so systemd only remembers them.
func (n *node) reboot() {
	for u := range n.units {
		if u != "homerslab-firewall-restore.service" {
			n.units[u] = [2]string{"not-found", "inactive"}
		}
	}
}

func (n *node) at(p string) string { return filepath.Join(n.root, p) }

func (n *node) write(p, s string) {
	n.t.Helper()
	if err := os.MkdirAll(filepath.Dir(n.at(p)), 0o755); err != nil {
		n.t.Fatal(err)
	}
	if err := os.WriteFile(n.at(p), []byte(s), 0o600); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) mkdir(p string) {
	n.t.Helper()
	if err := os.MkdirAll(n.at(p), 0o755); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) link(p, to string) {
	n.t.Helper()
	n.mkdir(filepath.Dir(p))
	if err := os.Symlink(to, n.at(p)); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) read(p string) string {
	b, err := os.ReadFile(n.at(p))
	if err != nil {
		return "missing"
	}
	return string(b)
}

func (n *node) host() migrate.Host { return migrate.Host{Root: n.root, Run: n.run} }

// run is systemctl and the shadow tools, on the node's own files.
func (n *node) run(_ context.Context, c host.Cmd) ([]byte, error) {
	line := c.String()
	switch {
	case strings.HasPrefix(line, "systemctl list-units"):
		var b strings.Builder
		for _, u := range slices.Sorted(maps.Keys(n.units)) {
			if strings.HasPrefix(u, migrate.OldPrefix) {
				fmt.Fprintf(&b, "%s %s %s running x\n", u, n.units[u][0], n.units[u][1])
			}
		}
		return []byte(b.String()), nil
	}
	n.ran = append(n.ran, line)
	switch c.Name {
	case "systemctl":
		if c.Args[0] == "stop" {
			for _, u := range c.Args[1:] {
				if s, ok := n.units[u]; ok {
					n.units[u] = [2]string{s[0], "inactive"}
				}
			}
		}
	case "groupmod": // --new-name NEW OLD
		n.edit("etc/group", func(f []string) []string {
			if f[0] == c.Args[2] {
				f[0] = c.Args[1]
			}
			return f
		})
	case "usermod": // --login NEW --home HOME OLD
		n.edit("etc/passwd", func(f []string) []string {
			if f[0] == c.Args[4] {
				f[0], f[5] = c.Args[1], c.Args[3]
			}
			return f
		})
		n.edit("etc/group", func(f []string) []string { // its supplementary groups' member lists
			members := strings.Split(f[3], ",")
			for i, m := range members {
				if m == c.Args[4] {
					members[i] = c.Args[1]
				}
			}
			f[3] = strings.Join(members, ",")
			return f
		})
	case "userdel": // and its own group with it, as Debian's and Fedora's do
		n.edit("etc/passwd", func(f []string) []string {
			if f[0] == c.Args[0] {
				return nil
			}
			return f
		})
		n.edit("etc/group", func(f []string) []string {
			if f[0] == c.Args[0] {
				return nil
			}
			return f
		})
	case "groupdel":
		n.edit("etc/group", func(f []string) []string {
			if f[0] == c.Args[0] {
				return nil
			}
			return f
		})
	}
	return nil, nil
}

func (n *node) edit(p string, each func([]string) []string) {
	var out []string
	for line := range strings.SplitSeq(strings.TrimSuffix(n.read(p), "\n"), "\n") {
		if f := each(strings.Split(line, ":")); f != nil {
			out = append(out, strings.Join(f, ":"))
		}
	}
	n.write(p, strings.Join(out, "\n")+"\n")
}

// tree is every file, folder and link under the node's root, with its content or target.
func (n *node) tree() map[string]string {
	n.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(n.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(n.root, p)
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			out[rel] = "→ " + to
		case e.IsDir():
			out[rel] = "/"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		n.t.Fatal(err)
	}
	return out
}

// A node HomersLab installed takes Homeward's names: its users keep their fixed ids, its folders move whole with the
// sealed credentials in them, its old units are stopped (the firewall table's kept: the engines require it) and their
// links and the agent's derived files go, the mesh's drop-ins keep their content, and the agent is left its mark.
func TestANodeHomersLabInstalledTakesHomewardsNames(t *testing.T) {
	testenv.Isolate(t)
	n := oldNode(t)
	r, err := migrate.Run(context.Background(), n.host())
	if err != nil {
		t.Fatal(err)
	}
	if want := "homeward:x:60600:60600:HomersLab manager:/var/lib/homeward:/usr/sbin/nologin\n" +
		"homeward-edge:x:60601:60601:HomersLab edge:/var/lib/homeward-edge:/usr/sbin/nologin\n" +
		"homeward-read:x:60602:60602:HomersLab compose reads:/:/usr/sbin/nologin\n" +
		"homeward-home:x:60603:60603:HomersLab Home:/:/usr/sbin/nologin\n" +
		"homeward-ldaps:x:60604:60604:HomersLab LDAPS:/:/usr/sbin/nologin\n"; !strings.Contains(n.read("etc/passwd"), want) {
		t.Errorf("the users are\n%s", n.read("etc/passwd"))
	}
	if want := "homeward:x:60600:\nhomeward-edge:x:60601:homeward\nhomeward-read:x:60602:\nhomeward-home:x:60603:\nhomeward-ldaps:x:60604:\n"; !strings.Contains(n.read("etc/group"), want) {
		t.Errorf("the groups are\n%s", n.read("etc/group"))
	}
	for p, want := range map[string]string{
		"var/lib/homeward-agent/agent.json":                                          `{"enforcing":true}`,
		"var/lib/homeward-agent/firewall.nft":                                        "table inet homerslab {}\n",
		"var/lib/homeward-agent/credentials/manager-home-client-secret.cred":         "sealed: the client's secret",
		"var/lib/homeward-agent/edge/cert-example.test.cred":                         "sealed: a certificate",
		"var/lib/homeward-agent/apps/ledger/compose.yaml":                            "services: {web: {image: x}}\n",
		"var/lib/homeward-agent/apps/ledger/credentials/db-password.cred":            "sealed: the app's",
		"var/lib/homeward-agent/apps/ledger/homerslab.yaml":                          "missing", // the agent writes homeward.yaml
		"var/lib/homeward/manager.db":                                                "the household",
		"var/lib/homeward-edge/caddy/locks/x":                                        "",
		"var/lib/homeward-agent/" + migrate.Mark:                                     "homerslab\n",
		"etc/systemd/system/netbird.service.d/10-homeward.conf":                      "[Service]\nEnvironment=NB_DISABLE_NAT_MAPPER=true\n",
		"etc/systemd/system/netbird.service.d/11-homeward-socket.conf":               "[Service]\nRuntimeDirectory=netbird\n",
		"etc/systemd/system/netbird.service.d/20-own.conf":                           "# someone's own\n",
		"etc/systemd/system/netbird.service.d/10-homerslab.conf":                     "missing",
		"etc/systemd/system/homerslab-edge.service.d/50-homerslab-certificates.conf": "missing",
		"etc/homeward/units/homerslab-app-ledger.service":                            "missing",
	} {
		if got := n.read(p); got != want {
			t.Errorf("/%s = %q, want %q", p, got, want)
		}
	}
	tree := n.tree()
	for p := range tree {
		if strings.Contains(p, "homerslab") && !strings.HasSuffix(p, "firewall.nft") && !strings.HasSuffix(p, migrate.Mark) {
			t.Errorf("/%s is left", p)
		}
	}
	for _, p := range []string{"var/lib/homeward-storage/media", "etc/homeward/units", "etc/systemd/system/multi-user.target.wants/sshd.service"} {
		if _, ok := tree[p]; !ok {
			t.Errorf("/%s is gone", p)
		}
	}
	if migrate.Pending(n.root) {
		t.Error("the node still has HomersLab's folders")
	}
	wantStopped := []string{"homerslab-admin.socket", "homerslab-agent.service", "homerslab-app-ledger.service", "homerslab-authentik.service",
		"homerslab-edge.service", "homerslab-home.service", "homerslab-manager.service", "homerslab-storage-media.service"}
	if !slices.Equal(r.Stopped, wantStopped) {
		t.Errorf("stopped %q, want %q (never the firewall table's, never a unit only remembered)", r.Stopped, wantStopped)
	}
	if n.units["homerslab-firewall-restore.service"][1] != "active" {
		t.Error("the firewall table's unit was stopped: the engines require it")
	}
	if i, j := slices.Index(n.ran, "systemctl stop "+strings.Join(wantStopped, " ")), slices.IndexFunc(n.ran, func(l string) bool {
		return strings.HasPrefix(l, "usermod")
	}); i < 0 || j < i {
		t.Errorf("the old units weren't stopped before their users were renamed: %q", n.ran)
	}
	if last := n.ran[len(n.ran)-3:]; last[0] != "systemctl daemon-reload" || last[1] != "systemd-tmpfiles --create homeward.conf" ||
		last[2] != "systemctl --no-block try-restart homeward-admin.socket homeward-home.socket" {
		t.Errorf("the run ended with %q, want systemd reloaded, and the new users' folders and sockets made again", last)
	}
}

// A second run, and a run on a node Homeward installed, change nothing and say so.
func TestANodeWithNothingOfHomersLabsIsLeftAlone(t *testing.T) {
	testenv.Isolate(t)
	n := oldNode(t)
	if _, err := migrate.Run(context.Background(), n.host()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(n.at("var/lib/homeward-agent/" + migrate.Mark)); err != nil { // the agent did its part
		t.Fatal(err)
	}
	n.reboot()
	n.units["homerslab-other.service"] = [2]string{"loaded", "active"} // another app of the maker's
	before, ran := n.tree(), len(n.ran)
	r, err := migrate.Run(context.Background(), n.host())
	if err != nil || r.Did() {
		t.Fatalf("a second run did %+v, %v", r, err)
	}
	if !maps.Equal(n.tree(), before) || len(n.ran) != ran {
		t.Errorf("a second run changed the node, or ran %q", n.ran[ran:])
	}
	if n.units["homerslab-other.service"][1] != "active" {
		t.Error("a unit named homerslab-* was stopped on a node with nothing of HomersLab's")
	}
}

// Pending fails closed: an old folder that can't be looked at (EACCES, EIO) counts as there, never as moved.
func TestPendingCountsAFolderItCantLookAt(t *testing.T) {
	testenv.Isolate(t)
	root := t.TempDir()
	for _, e := range []error{syscall.EACCES, syscall.EIO} {
		migrate.SetLstat(t, func(string) (fs.FileInfo, error) { return nil, e })
		if !migrate.Pending(root) {
			t.Errorf("a folder that can't be looked at (%v) reads as moved", e)
		}
	}
	migrate.SetLstat(t, func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist })
	if migrate.Pending(root) {
		t.Error("folders that aren't there read as pending")
	}
}

// A run that stops anywhere (a crash, a power cut) leaves a node the next run finishes as one that never stopped would:
// no folder is lost or doubled, no user made twice, and the agent's mark is there before what it stands for goes.
func TestARunThatStopsAnywhereIsFinishedByTheNext(t *testing.T) {
	testenv.Isolate(t)
	ref := oldNode(t)
	var steps []string
	if _, err := migrate.Run(context.Background(), migrate.WithFail(ref.host(), func(s string) error {
		steps = append(steps, s)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	want := ref.tree()
	if len(steps) < 15 {
		t.Fatalf("only %d steps: %q", len(steps), steps)
	}
	for k, at := range steps {
		n := oldNode(t)
		crash := errors.New("the power went")
		seen := 0
		_, err := migrate.Run(context.Background(), migrate.WithFail(n.host(), func(string) error {
			if seen++; seen == k+1 {
				return crash
			}
			return nil
		}))
		if !errors.Is(err, crash) {
			t.Fatalf("stopped at %q: %v", at, err)
		}
		n.reboot() // a power cut: at the next boot the old package's units are gone
		ran := len(n.ran)
		if _, err := migrate.Run(context.Background(), n.host()); err != nil {
			t.Errorf("after a stop at %q, the next run failed: %v", at, err)
			continue
		}
		// The follow-ups too, whichever step it stopped at: the new users' folders and sockets are made again.
		var follow []string
		for _, c := range migrate.FollowUps {
			follow = append(follow, c.String())
		}
		if next := n.ran[ran:]; len(next) < len(follow) || !slices.Equal(next[len(next)-len(follow):], follow) {
			t.Errorf("after a stop at %q, the next run ran %q, want it to end with %q", at, next, follow)
		}
		// The sockets wait for the migration's unit: a restart it waited for would never end.
		if last := follow[len(follow)-1]; last != "systemctl --no-block try-restart homeward-admin.socket homeward-home.socket" {
			t.Errorf("the sockets are restarted with %q, want it queued", last)
		}
		got := n.tree()
		for p := range mapsUnion(want, got) {
			if want[p] != got[p] {
				t.Errorf("after a stop at %q, /%s is %q, want %q", at, p, got[p], want[p])
			}
		}
	}
}

func mapsUnion(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// sysusers can run first (a package manager that makes the users before its scriptlets): it finds the fixed ids
// taken by the old users and makes the new ones with others. Those go, and the old ones take their names, ids kept.
func TestUsersSysusersMadeFirstWithOtherIDsGive(t *testing.T) {
	testenv.Isolate(t)
	n := oldNode(t)
	n.write("etc/passwd", n.read("etc/passwd")+"homeward:x:989:989:Homeward manager:/var/lib/homeward:/usr/sbin/nologin\n")
	n.write("etc/group", n.read("etc/group")+"homeward:x:989:\n")
	if _, err := migrate.Run(context.Background(), n.host()); err != nil {
		t.Fatal(err)
	}
	if p := n.read("etc/passwd"); strings.Contains(p, ":989:") || !strings.Contains(p, "\nhomeward:x:60600:60600:") {
		t.Errorf("the users are\n%s", p)
	}
	if g := n.read("etc/group"); strings.Contains(g, ":989:") || !strings.Contains(g, "\nhomeward:x:60600:") {
		t.Errorf("the groups are\n%s", g)
	}
	if !slices.Contains(n.ran, "userdel homeward") {
		t.Errorf("ran %q", n.ran)
	}
}

// A new folder that already holds files beside the old one is a node someone looks at: the run changes nothing at all,
// and says which folders. The agent and the manager refuse to start meanwhile (Pending).
func TestBothTheOldAndTheNewFolderHoldingFilesChangesNothing(t *testing.T) {
	testenv.Isolate(t)
	n := oldNode(t)
	n.write("var/lib/homeward/manager.db", "a new, empty household")
	before := n.tree()
	_, err := migrate.Run(context.Background(), n.host())
	if !errors.Is(err, migrate.ErrConflict) || !strings.Contains(err.Error(), "/var/lib/homerslab and /var/lib/homeward") {
		t.Fatalf("got %v, want the conflict named", err)
	}
	if !maps.Equal(n.tree(), before) || len(n.ran) != 0 {
		t.Errorf("a refused run changed the node, or ran %q", n.ran)
	}
	if !migrate.Pending(n.root) {
		t.Error("the node isn't pending")
	}
}

// Pending is a node with any of HomersLab's folders left; a node Homeward installed has none.
func TestPendingIsAnyOfHomersLabsFolders(t *testing.T) {
	testenv.Isolate(t)
	root := t.TempDir()
	if migrate.Pending(root) {
		t.Error("an empty root is pending")
	}
	for _, d := range migrate.Dirs {
		if err := os.MkdirAll(filepath.Join(root, d.New), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if migrate.Pending(root) {
		t.Error("a node with Homeward's folders is pending")
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/homerslab"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !migrate.Pending(root) {
		t.Error("a node with /etc/homerslab isn't pending")
	}
}
