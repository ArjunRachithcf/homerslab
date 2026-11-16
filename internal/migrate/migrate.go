// Package migrate moves a node installed under HomersLab's names to Homeward's (#250, ADR 0022): its users and groups,
// its folders (the agent's, with the sealed credentials in it, the manager's, the edge's, the shares' mount points and
// the apps' units), and its units' links. Every name it moves is one the package owns on the host; the names Homeward
// keeps in other software (the identity provider's, the mesh's, the engine's, the firewall table, DNS records,
// cookies, the identity headers) keep the maker's prefix and are left as they are.
//
// It runs as root, as `homeward agent migrate`, from homeward-migrate.service: at boot before systemd-sysusers, the
// sockets, the firewall table and the agent, which requires it; and whenever the agent's unit starts. A node with
// nothing of HomersLab's left is a no-op. Each step is idempotent, and leaves the node in a state the next run
// finishes, wherever a run stopped (a crash, a power cut): a folder moves in one rename(2), with the sealed
// credentials in it, so it is under its old name or its new one, never both or neither; a user is renamed in place,
// keeping its fixed uid, so the files it owns stay its own. What the agent derives from its state (apps' and shares'
// units, the drop-ins of its own units) isn't moved but dropped: the run leaves a mark in the agent's state, and the
// agent writes them again under the new names as it starts (agent.Renamed), and removes the mark only once that is
// done.
package migrate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homeward/internal/atomicfile"
	"github.com/ArjunRachithcf/homeward/internal/host"
)

// User is one of the package's users (packaging/sysusers.d), under its old and new name, with the fixed uid and gid
// they share (#190) and the new home folder.
type User struct {
	Old, New string
	ID       int
	Home     string
}

// Users are the package's users and their groups, each of the same name and id.
var Users = []User{
	{"homerslab", "homeward", 60600, "/var/lib/homeward"},
	{"homerslab-edge", "homeward-edge", 60601, "/var/lib/homeward-edge"},
	{"homerslab-read", "homeward-read", 60602, "/"},
	{"homerslab-home", "homeward-home", 60603, "/"},
	{"homerslab-ldaps", "homeward-ldaps", 60604, "/"},
}

// Dir is a folder that moves, relative to the machine's root.
type Dir struct{ Old, New string }

// Dirs are the folders that move whole: the agent's state (agent.json and every sealed credential), the manager's
// (its database), the edge's storage, the shares' mount points (unmounted: their units are stopped first) and the
// folder of the apps' units.
var Dirs = []Dir{
	{OldAgentState, AgentState},
	{"var/lib/homerslab", "var/lib/homeward"},
	{"var/lib/homerslab-edge", "var/lib/homeward-edge"},
	{"var/lib/homerslab-storage", "var/lib/homeward-storage"},
	{"etc/homerslab", "etc/homeward"},
}

// Dropped are folders that go: the agent's cache, the exec-only copy of itself that it makes again.
var Dropped = []string{"var/cache/homerslab-agent"}

// Renamed are files that keep their content under a new name: the mesh clients' drop-ins, which the agent checks and
// puts back if they differ from what it would write.
var Renamed = []Dir{
	{"etc/systemd/system/netbird.service.d/10-homerslab.conf", "etc/systemd/system/netbird.service.d/10-homeward.conf"},
	{"etc/systemd/system/netbird.service.d/11-homerslab-socket.conf", "etc/systemd/system/netbird.service.d/11-homeward-socket.conf"},
	{"etc/systemd/system/tailscaled.service.d/10-homerslab-socket.conf", "etc/systemd/system/tailscaled.service.d/10-homeward-socket.conf"},
}

// DropIns are the old units' drop-in folders the agent wrote (the edge's certificates, the sign-in's secrets, its LDAP
// outpost's token, its LDAPS port's certificate), which go: the agent writes them again for the new units.
var DropIns = []string{
	"etc/systemd/system/homerslab-edge.service.d",
	"etc/systemd/system/homerslab-authentik.service.d",
	"etc/systemd/system/homerslab-ldap.service.d",
	"etc/systemd/system/homerslab-ldaps.service.d",
}

// OldPrefix is how every old unit's name begins, the package's and those the agent wrote (apps', shares', backups',
// restores'); OldOverride is the old name of each app's compose file of the agent's.
const (
	OldPrefix   = "homerslab-"
	OldOverride = "homerslab.yaml"
)

// keptRunning is the one old unit a run never stops: the engines and the mesh client require the firewall table's
// unit, so stopping it would stop them. It has done its work at boot and holds nothing.
const keptRunning = "homerslab-firewall-restore.service"

// Mark is the file a run leaves in the agent's new state folder: the agent writes its derived files again, and removes
// it once they are all in place.
const Mark = "renamed-from-homerslab"

// AgentState is the agent's state folder, relative to the machine's root, where Mark goes; OldAgentState its name
// before #250.
const (
	AgentState    = "var/lib/homeward-agent"
	OldAgentState = "var/lib/homerslab-agent"
)

// Host is the machine a run changes.
type Host struct {
	// Root is the machine's root: "/" on a real machine.
	Root string
	// Run runs systemctl and the shadow tools (groupmod, usermod, groupdel, userdel).
	Run host.Runner
	Log *slog.Logger
	// Users are the users and groups a run renames: Users when nil (another list only in tests, which run the real
	// shadow tools on users of their own).
	Users []User
	// UsersOnly renames the users and touches no folder and no unit: for tests on a real machine's /etc only.
	UsersOnly bool
	// fail, in tests, makes the named step fail before it changes anything: a crash at that point.
	fail func(step string) error
}

// Result is what a run did.
type Result struct {
	Stopped []string // the old units it stopped
	Users   []string // the users and groups it renamed or replaced
	Moved   []string // the folders and files it moved
	Removed []string // what it removed
}

// Did says whether the run changed anything.
func (r Result) Did() bool {
	return len(r.Stopped)+len(r.Users)+len(r.Moved)+len(r.Removed) > 0
}

// Pending says whether root still has a folder of HomersLab's that a run moves: the agent and the manager refuse to
// start over one, since without it they'd start as a new node (with a new household, and first-time setup open).
func Pending(root string) bool {
	for _, d := range Dirs {
		// Fails closed: only a folder that isn't there has moved (EACCES or EIO count as there).
		if _, err := lstat(filepath.Join(root, d.Old)); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// lstat is os.Lstat; tests give Pending the errors (EACCES, EIO) root never meets.
var lstat = os.Lstat

// ErrConflict is an old folder whose new one already holds something: the run stops before it changes the node's
// files, and the node stays as it is until someone looks (the journal says which).
var ErrConflict = errors.New("both the old and the new folder hold files")

// Run moves the node at h.Root to Homeward's names. It is a no-op on a node with nothing of HomersLab's.
func Run(ctx context.Context, h Host) (Result, error) {
	if h.Log == nil {
		h.Log = slog.New(slog.DiscardHandler)
	}
	var r Result
	// Nothing at all, not even a stop, on a node with nothing of HomersLab's: another app of the maker's may have
	// units named homerslab-* of its own.
	seen, err := h.evidence()
	if err != nil || !seen {
		return r, err
	}
	if !h.UsersOnly {
		// Every check first, so a conflict changes nothing.
		for _, d := range Dirs {
			if err := h.check(d); err != nil {
				return r, err
			}
		}
		if err := h.stop(ctx, &r); err != nil {
			return r, err
		}
	}
	if err := h.users(ctx, &r); err != nil {
		return r, err
	}
	if !h.UsersOnly {
		if err := h.files(&r); err != nil {
			return r, err
		}
	}
	// Then, after any change and at every run until the agent has done its part (the mark is there): systemd reloads,
	// and what tmpfiles and systemd made for a user sysusers made first, with another id (Home's projection, the
	// sockets), is made again for the renamed one. Each is idempotent, and a socket that isn't running is left so: a
	// run that stopped after the users' step is finished here by the next.
	if _, err := os.Stat(h.at(path.Join(AgentState, Mark))); err != nil && !r.Did() {
		return r, nil
	}
	for _, c := range FollowUps {
		if err := h.step(c.String()); err != nil {
			return r, err
		}
		if _, err := h.Run(ctx, c); err != nil {
			return r, err
		}
	}
	if r.Did() {
		h.Log.Info("the node took Homeward's names", "stopped", r.Stopped, "users", r.Users, "moved", r.Moved, "removed", r.Removed)
	}
	return r, nil
}

// FollowUps end every run that changed something, or that finds the mark.
var FollowUps = []host.Cmd{
	{Name: "systemctl", Args: []string{"daemon-reload"}},
	{Name: "systemd-tmpfiles", Args: []string{"--create", "homeward.conf"}},
	// Queued, never waited for: the sockets are ordered after homeward-migrate.service, so a restart this run waited
	// for would wait for the run itself, on an upgrade without a reboot.
	{Name: "systemctl", Args: []string{"--no-block", "try-restart", "homeward-admin.socket", "homeward-home.socket"}},
}

// files moves the folders, drops what goes, leaves the mark, and removes the agent's derived files.
func (h Host) files(r *Result) error {
	for _, d := range Dirs {
		if err := h.move(d, r); err != nil {
			return err
		}
	}
	for _, d := range Dropped {
		if err := h.removeAll(d, r); err != nil {
			return err
		}
	}
	derived, err := h.derived()
	if err != nil {
		return err
	}
	if len(derived) > 0 || r.Did() {
		if err := h.mark(); err != nil {
			return err
		}
	}
	for _, f := range Renamed {
		if err := h.rename(f, r); err != nil {
			return err
		}
	}
	for _, p := range derived {
		if err := h.removeAll(p, r); err != nil {
			return err
		}
	}
	return nil
}

// evidence says whether anything of HomersLab's is on the node: an old user or group, folder, file the agent wrote or
// link, or the mark of a run the agent hasn't followed yet. A path that can't be looked at counts.
func (h Host) evidence() (bool, error) {
	users, err := readIDs(h.at("etc/passwd"))
	if err != nil {
		return false, err
	}
	groups, err := readIDs(h.at("etc/group"))
	if err != nil {
		return false, err
	}
	list := h.Users
	if list == nil {
		list = Users
	}
	for _, u := range list {
		_, user := users[u.Old]
		_, group := groups[u.Old]
		if user || group {
			return true, nil
		}
	}
	if h.UsersOnly {
		return false, nil
	}
	paths := append([]string{path.Join(AgentState, Mark)}, Dropped...)
	for _, d := range append(slices.Clone(Dirs), Renamed...) {
		paths = append(paths, d.Old)
	}
	for _, p := range paths {
		if _, err := os.Lstat(h.at(p)); !errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
	}
	derived, err := h.derived()
	return len(derived) > 0, err
}

func (h Host) at(p string) string { return filepath.Join(h.Root, p) }

func (h Host) step(name string) error {
	if h.fail != nil {
		return h.fail(name)
	}
	return nil
}

// check refuses a folder whose old and new names both hold files.
func (h Host) check(d Dir) error {
	if _, err := os.Lstat(h.at(d.Old)); err != nil {
		return nil
	}
	empty, err := emptyTree(h.at(d.New))
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("/%s and /%s: %w", d.Old, d.New, ErrConflict)
	}
	return nil
}

// emptyTree says whether p is missing, or a folder of folders with no file anywhere: what systemd and tmpfiles make
// (StateDirectory=, /etc/homeward/units) before a run.
func emptyTree(p string) (bool, error) {
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	case !fi.IsDir():
		return false, nil
	}
	empty := true
	err = filepath.WalkDir(p, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			empty = false
			return fs.SkipAll
		}
		return nil
	})
	return empty, err
}

// stop stops every old unit systemd has loaded, but the firewall table's: an old agent would fight the new one over
// the table, an old app's unit over its project. Queued starts (at boot) are cancelled with them.
func (h Host) stop(ctx context.Context, r *Result) error {
	out, err := h.Run(ctx, host.Cmd{Name: "systemctl", Args: []string{"list-units", "--all", "--plain", "--no-legend", "--full", OldPrefix + "*"}})
	if err != nil {
		return err
	}
	var units []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		// UNIT LOAD ACTIVE SUB DESCRIPTION. A unit whose file is gone and that isn't running is only remembered.
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "●"))
		if len(f) < 3 || !strings.HasPrefix(f[0], OldPrefix) || f[0] == keptRunning || f[1] != "loaded" && f[2] == "inactive" {
			continue
		}
		units = append(units, f[0])
	}
	if len(units) == 0 {
		return nil
	}
	slices.Sort(units)
	if err := h.step("stop"); err != nil {
		return err
	}
	if _, err := h.Run(ctx, host.Cmd{Name: "systemctl", Args: append([]string{"stop"}, units...)}); err != nil {
		return err
	}
	r.Stopped = units
	return nil
}

// users renames each old user and group in place, keeping its fixed id. A new one that sysusers made first, with
// another id because the old one held the fixed id (sysusers falls back to a free one), goes first: it owns nothing
// yet, since nothing of Homeward's runs before a run.
func (h Host) users(ctx context.Context, r *Result) error {
	list := h.Users
	if list == nil {
		list = Users
	}
	for _, u := range list {
		// One step at a time, each from the files as they are now: userdel may take the user's group with it.
		for range 4 {
			cmd, args, err := h.userStep(u)
			if err != nil {
				return err
			}
			if cmd == "" {
				break
			}
			if err := h.step(cmd + " " + u.Old); err != nil {
				return err
			}
			if _, err := h.Run(ctx, host.Cmd{Name: cmd, Args: args}); err != nil {
				return err
			}
			r.Users = append(r.Users, cmd+" "+strings.Join(args, " "))
		}
	}
	return nil
}

// userStep is the next command that moves u to its new name, or "" when there is none: done, or nothing of it here.
// A new user or group of another id goes only while the old one holds the fixed id; one of the fixed id is kept.
func (h Host) userStep(u User) (string, []string, error) {
	users, err := readIDs(h.at("etc/passwd"))
	if err != nil {
		return "", nil, err
	}
	groups, err := readIDs(h.at("etc/group"))
	if err != nil {
		return "", nil, err
	}
	oldU, hasOldU := users[u.Old]
	newU, hasNewU := users[u.New]
	oldG, hasOldG := groups[u.Old]
	newG, hasNewG := groups[u.New]
	switch {
	case hasOldU && oldU == u.ID && hasNewU && newU != u.ID:
		return "userdel", []string{u.New}, nil
	case hasOldG && oldG == u.ID && hasNewG && newG != u.ID:
		return "groupdel", []string{u.New}, nil
	case hasOldG && !hasNewG:
		return "groupmod", []string{"--new-name", u.New, u.Old}, nil
	case hasOldU && !hasNewU:
		return "usermod", []string{"--login", u.New, "--home", u.Home, u.Old}, nil
	}
	return "", nil, nil
}

// readIDs reads the names and ids of a passwd or group file.
func readIDs(file string) (map[string]int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	ids := map[string]int{}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if id, err := strconv.Atoi(f[2]); err == nil {
			ids[f[0]] = id
		}
	}
	return ids, nil
}

// move renames an old folder to its new name, in one rename(2), over an empty tree systemd or tmpfiles made there.
func (h Host) move(d Dir, r *Result) error {
	if _, err := os.Lstat(h.at(d.Old)); err != nil {
		return nil
	}
	if err := h.check(d); err != nil {
		return err
	}
	if err := h.step("move " + d.Old); err != nil {
		return err
	}
	// Between the two, the new name is missing and the old one there: the next run renames it.
	if err := os.RemoveAll(h.at(d.New)); err != nil {
		return err
	}
	if err := h.step("move " + d.Old + ", its new name cleared"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.at(d.New)), 0o755); err != nil {
		return err
	}
	if err := os.Rename(h.at(d.Old), h.at(d.New)); err != nil {
		return err
	}
	syncDir(filepath.Dir(h.at(d.New)))
	r.Moved = append(r.Moved, "/"+d.Old+" → /"+d.New)
	return nil
}

// rename gives a file its new name, or drops the old one when the new one is already there.
func (h Host) rename(f Dir, r *Result) error {
	if _, err := os.Lstat(h.at(f.Old)); err != nil {
		return nil
	}
	if err := h.step("rename " + f.Old); err != nil {
		return err
	}
	if _, err := os.Lstat(h.at(f.New)); err == nil {
		return h.removeAll(f.Old, r)
	}
	if err := os.Rename(h.at(f.Old), h.at(f.New)); err != nil {
		return err
	}
	syncDir(filepath.Dir(h.at(f.New)))
	r.Moved = append(r.Moved, "/"+f.Old+" → /"+f.New)
	return nil
}

func (h Host) removeAll(p string, r *Result) error {
	if _, err := os.Lstat(h.at(p)); err != nil {
		return nil
	}
	if err := h.step("remove " + p); err != nil {
		return err
	}
	if err := os.RemoveAll(h.at(p)); err != nil {
		return err
	}
	syncDir(filepath.Dir(h.at(p)))
	r.Removed = append(r.Removed, "/"+p)
	return nil
}

// derived are the old files the agent wrote from its state, which go: the old units' drop-in folders, the old units in
// the apps' units folder, each app's old compose file of the agent's, and every link to an old unit under
// /etc/systemd/system (what `systemctl enable` made).
func (h Host) derived() ([]string, error) {
	var out []string
	for _, d := range DropIns {
		if _, err := os.Lstat(h.at(d)); err == nil {
			out = append(out, d)
		}
	}
	units, _ := os.ReadDir(h.at("etc/homeward/units"))
	for _, e := range units {
		if strings.HasPrefix(e.Name(), OldPrefix) {
			out = append(out, path.Join("etc/homeward/units", e.Name()))
		}
	}
	apps, _ := os.ReadDir(h.at(path.Join(AgentState, "apps")))
	for _, e := range apps {
		p := path.Join(AgentState, "apps", e.Name(), OldOverride)
		if _, err := os.Lstat(h.at(p)); err == nil {
			out = append(out, p)
		}
	}
	links, err := h.oldLinks("etc/systemd/system", 2)
	if err != nil {
		return nil, err
	}
	return append(out, links...), nil
}

// oldLinks are the links to old units in dir and the folders under it, depth levels down.
func (h Host) oldLinks(dir string, depth int) ([]string, error) {
	entries, err := os.ReadDir(h.at(dir))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var out []string
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0 && strings.HasPrefix(e.Name(), OldPrefix):
			out = append(out, p)
		case e.IsDir() && depth > 1:
			more, err := h.oldLinks(p, depth-1)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
	}
	return out, nil
}

// mark leaves Mark in the agent's state, before anything it stands for is removed.
func (h Host) mark() error {
	p := h.at(path.Join(AgentState, Mark))
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := h.step("mark"); err != nil {
		return err
	}
	return atomicfile.Write(p, []byte("homerslab\n"))
}

// syncDir makes a folder's new entries survive a power cut; a failure leaves the rename done, which is what counts.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
