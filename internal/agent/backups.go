package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homeward/internal/appunit"
	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/engine"
	"github.com/ArjunRachithcf/homeward/internal/host"
	"github.com/ArjunRachithcf/homeward/internal/node"
)

// Backups on this node (#22; ADR 0010, amended for #90). The manager hands the agent the household's target, this
// node's repository key and the target's credentials; the agent seals each as a credential of its own (never handed
// back), and makes the node's repository. Every hour the manager's scheduler asks it to back up: for each app, exactly
// what its plan said (its declared folders, its declared dumps) and nothing else, and on the primary the platform's
// own state (the sign-in's database here, the manager's from the manager).
//
// restic runs in a unit of its own (systemd-run), which loads the sealed credentials into its credentials folder: the
// agent never holds them in the clear, and none is ever an argument or an environment variable. The unit reads apps'
// folders (CAP_DAC_READ_SEARCH and nothing else), writes only its cache, on the app-data disk, and a disk target.

// BackupTarget is the household's target, this node's name and repository key, and the target's credentials, as the
// manager hands them over.
type BackupTarget struct {
	Node        string             `json:"node"`
	Target      backup.Target      `json:"target"`
	Key         string             `json:"key"`
	Credentials backup.Credentials `json:"credentials"`
}

// AppData is one thing of an app's that its plan backs up: a folder in a service, or a dump its service writes with a
// declared command, as a file.
type AppData struct {
	Service string   `json:"service"`
	Path    string   `json:"path,omitempty"`
	Dump    []string `json:"dump,omitempty"`
	File    string   `json:"file,omitempty"`
	// Restore is the dump's own way back: a command in the same service that reads the dump on its standard input.
	Restore []string `json:"restore,omitempty"`
}

// key is the data's name among its app's: its service and its folder, or its dump's file.
func (d AppData) key() string {
	if d.Path != "" {
		return d.Service + ":" + d.Path
	}
	return d.Service + ":" + d.File
}

// backupState is the node's target and repository, as the agent keeps them in agent.json. None of it is secret.
type backupState struct {
	Node   string        `json:"node"`
	Target backup.Target `json:"target"`
	// Files are the credential IDs sealed for it, in the backup folder.
	Files []string `json:"files"`
	// Incomplete are the latest snapshots restic saved without some of their files: never a point to restore to.
	Incomplete []string `json:"incomplete,omitempty"`
	// Immutability is whether the target keeps the node's backups from whoever holds its credentials, as read when it
	// was set.
	Immutability backup.Immutability `json:"immutability"`
}

// maxIncomplete is how many incomplete snapshots the agent remembers; prune forgets older ones long before.
const maxIncomplete = 2000

// incomplete remembers that restic saved a snapshot without some of its files.
func (a *Agent) incomplete(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Backup == nil || !backup.ValidSnapshot(id) {
		return
	}
	b := a.state.Backup
	b.Incomplete = append(b.Incomplete, id)
	if n := len(b.Incomplete); n > maxIncomplete {
		b.Incomplete = slices.Clone(b.Incomplete[n-maxIncomplete:])
	}
	if err := a.save(); err != nil {
		a.log.Error("saving the agent's state", "err", err)
	}
}

// BackupResult is one item of a run: whose (an app, or the platform's), which data, and its snapshot or why not.
type BackupResult struct {
	App      string `json:"app,omitempty"`
	Platform string `json:"platform,omitempty"`
	Data     string `json:"data"`
	Snapshot string `json:"snapshot,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Backups are a run's results.
type Backups struct {
	Results []BackupResult `json:"results"`
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// validData says whether an app's backups are plain: a service by name, and a folder or a dump with its file.
func validData(data []AppData) bool {
	for _, d := range data {
		folder := d.Path != "" && d.Dump == nil && d.File == "" && backup.ValidPath(d.Path)
		dump := d.Path == "" && backup.ValidCommand(d.Dump) && backup.ValidFile(d.File)
		if !serviceName.MatchString(d.Service) || folder == dump || d.Restore != nil && (!dump || !backup.ValidCommand(d.Restore)) {
			return false
		}
	}
	return true
}

// The sign-in's database, as the package's compose file runs it.
var signInDump = []string{"/usr/bin/docker", "compose", "--project-name", "homerslab-authentik", "--file", "/usr/share/homeward/sign-in/authentik.compose.yaml",
	"exec", "-T", "postgresql", "pg_dump", "--username=authentik", "--clean", "--if-exists", "authentik"}

// backupCache is restic's cache, a state folder of the unit's on the app-data disk, counted in that disk's use.
var backupCache = filepath.Join(node.DataPath, "backup-cache")

func (a *Agent) backupDir() string { return filepath.Join(a.cfg.StateDir, "backup") }

// The longest the agent waits for one restic run: a first backup of a photo library takes hours.
const backupWait = 12 * time.Hour

// writes are what a run of restic may write besides its cache and a disk target: nothing for a backup; for a restore,
// the one volume's folder it restores into, or the folder a dump is written to for its app's restore command.
type writes struct {
	into  string // a volume's folder on the machine
	dumps bool   // restoreDir
}

// restoreDir is where a dump waits for its app's restore command: a state folder of the unit's, on the app-data disk,
// for root alone.
var restoreDir = filepath.Join(node.DataPath, "restore")

// repo is the node's repository for one run of restic, in a unit of its own, with the credentials sealed in dir: a
// new unit's name, and the credentials folder systemd makes for it, each time. The unit reads everything and writes
// only its cache and a disk target.
func (a *Agent) repo(st backupState, dir string) backup.Repo { return a.repoWriting(st, dir, writes{}) }

// repoWriting is repo, for a restore: the unit may also write w, and nothing else. Whatever a snapshot holds and
// whatever restic made of it, the rest of the machine is read-only to the run (ProtectSystem=strict; no device, no
// kernel setting), so no link and no name in a snapshot leads a restore out of the volume it restores.
func (a *Agent) repoWriting(st backupState, dir string, w writes) backup.Repo {
	restic := a.cfg.Restic
	if restic == "" {
		restic = backup.Restic
	}
	unit := fmt.Sprintf("homeward-backup-%d-%d", a.cfg.Now().UnixNano(), a.backupRuns.Add(1))
	r := backup.Repo{Restic: restic, Target: st.Target, Node: st.Node, Dir: "/run/credentials/" + unit + ".service", Cache: backupCache}
	ran := false
	r.Run = func(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
		if ran { // a unit's name is never used twice
			return nil, errors.New("backup: one run of restic per unit")
		}
		ran = true
		state, caps := strings.TrimPrefix(backupCache, "/var/lib/"), "CAP_DAC_READ_SEARCH"
		if w.dumps {
			state += " " + strings.TrimPrefix(restoreDir, "/var/lib/")
		}
		if w.into != "" {
			// A volume's files are the app's users': the run may write them, and give them their owners and modes back.
			caps += " CAP_DAC_OVERRIDE CAP_CHOWN CAP_FOWNER CAP_FSETID"
		}
		args := []string{"--unit=" + unit, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
			"-p", "StateDirectory=" + state, "-p", "ProtectSystem=strict", "-p", "PrivateTmp=yes",
			"-p", "NoNewPrivileges=yes", "-p", "CapabilityBoundingSet=" + caps}
		if st.Target.Kind == backup.Disk {
			args = append(args, "-p", "ReadWritePaths="+st.Target.Path)
		}
		if w.into != "" || w.dumps {
			args = append(args, "-p", "StateDirectoryMode=0700", "-p", "PrivateDevices=yes", "-p", "ProtectKernelTunables=yes",
				"-p", "ProtectControlGroups=yes", "-p", "ProtectKernelModules=yes")
		}
		if w.into != "" {
			args = append(args, "-p", "ReadWritePaths="+w.into)
		}
		for _, f := range st.Files {
			args = append(args, "-p", "LoadCredentialEncrypted="+f+":"+filepath.Join(dir, f+".cred"))
		}
		for _, e := range env {
			args = append(args, "-E", e)
		}
		ctx, cancel := context.WithTimeout(ctx, backupWait)
		defer cancel()
		out, err := a.cfg.Run(ctx, host.Cmd{Name: "systemd-run", Args: append(append(args, "--"), argv...), Stdin: stdin})
		if ctx.Err() != nil {
			// Only systemd-run was ended: its unit, and restic in it, are stopped too, so no run outlives its turn.
			stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if serr := a.run(stop, "systemctl", "stop", unit+".service"); serr != nil {
				a.log.Error("stopping a backup's unit", "unit", unit, "err", serr)
			}
		}
		return out, err
	}
	return r
}

// unlock clears the stale locks a crashed run left, as a run's first step; a run goes on whatever it says.
func (a *Agent) unlock(ctx context.Context, st backupState) {
	if err := a.repo(st, a.backupDir()).Unlock(ctx); err != nil {
		a.log.Error("clearing the repository's stale locks", "err", err)
	}
}

// putBackups takes the household's target, seals this node's key and the target's credentials, and makes the node's
// repository; a repository already there must open with the key. Until it does, the node's backups stay as they were.
func (a *Agent) putBackups(w http.ResponseWriter, r *http.Request) {
	var req BackupTarget
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	files, err := backup.Files(req.Target, []byte(req.Key), req.Credentials)
	if err == nil {
		_, err = req.Target.Location(req.Node)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "bad-target")
		return
	}
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	ctx := r.Context()
	next := a.backupDir() + ".next"
	st := backupState{Node: req.Node, Target: req.Target}
	if err := os.RemoveAll(next); err != nil {
		a.internal(w, "the backup credentials", err)
		return
	}
	if err := os.MkdirAll(next, 0o700); err != nil {
		a.internal(w, "the backup credentials", err)
		return
	}
	for id, b := range files {
		if err := a.sealAt(ctx, filepath.Join(next, id+".cred"), id, b); err != nil {
			a.internal(w, "sealing a backup credential", err)
			return
		}
		st.Files = append(st.Files, id)
	}
	slices.Sort(st.Files)
	// The new credentials are tried from their own folder, then take the old ones' place.
	there, err := a.repo(st, next).Exists(ctx)
	if err == nil && !there {
		err = a.repo(st, next).Init(ctx)
	}
	if err != nil {
		a.log.Error("making the node's repository at the target", "err", err)
		_ = os.RemoveAll(next)
		if errors.Is(err, backup.ErrWrongKey) {
			fail(w, http.StatusConflict, "wrong-key")
			return
		}
		fail(w, http.StatusBadGateway, "target-failed")
		return
	}
	// Whether a box taken over could delete them, read (and on a locked bucket, every object locked) in a unit of its own
	// with the new credentials, as restic's runs are.
	st.Immutability = a.lockRun(ctx, st, next)
	// The old folder is put aside, not removed, until the state names the new one: a failure puts it back.
	old := a.backupDir() + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(a.backupDir(), old); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.internal(w, "the backup credentials", err)
		return
	}
	if err := os.Rename(next, a.backupDir()); err != nil {
		_ = os.Rename(old, a.backupDir())
		a.internal(w, "the backup credentials", err)
		return
	}
	a.mu.Lock()
	prev := a.state.Backup
	a.state.Backup = &st
	err = a.save()
	if err != nil {
		a.state.Backup = prev
	}
	a.mu.Unlock()
	if err != nil {
		_ = os.RemoveAll(a.backupDir())
		_ = os.Rename(old, a.backupDir())
		a.internal(w, "saving the agent's state", err)
		return
	}
	_ = os.RemoveAll(old)
	loc, _ := st.Target.Location(st.Node)
	a.log.Info("the node's backups go to the household's target", "repository", loc, "immutable", st.Immutability.Immutable, "why", st.Immutability.Why)
	writeJSON(w, http.StatusOK, TargetSet{Repository: loc, Immutability: st.Immutability})
}

// Self is the agent's own program, which a lock run is (`homeward agent backup-lock`), in restic's unit.
const Self = "/usr/bin/homeward"

// lockRun reads whether the target keeps the node's backups from whoever holds its credentials and, on a bucket locked
// in compliance mode, locks every object of the node's repository again when it is due (backup.Lock): in a unit of its
// own with the credentials sealed in dir, as restic runs, so the agent never holds them in the clear. A run that fails
// says why in a word; a lock that has run out says so.
func (a *Agent) lockRun(ctx context.Context, st backupState, dir string) backup.Immutability {
	now := a.cfg.Now().UTC()
	if st.Target.Kind == backup.Disk || st.Target.Kind == backup.SFTP { // nothing to ask
		im, _ := backup.Lock(ctx, st.Target, backup.Credentials{}, st.Node, st.Immutability, now)
		return im
	}
	req, err := json.Marshal(backup.LockRequest{Target: st.Target, Node: st.Node, Was: st.Immutability})
	if err != nil {
		return backup.Immutability{Why: "unknown", At: now}
	}
	out, err := a.repo(st, dir).Run(ctx, []string{Self, "agent", "backup-lock"}, []string{"TZ=UTC"}, req)
	var im backup.Immutability
	if jerr := json.Unmarshal(out, &im); jerr != nil || im.Why == "" {
		im = st.Immutability.Now(now) // what was locked stays locked until it runs out
		if !im.Immutable {
			im = backup.Immutability{Why: "unknown", At: now}
		}
	}
	if err != nil {
		a.log.Error("the backup target's lock run", "err", err, "why", im.Why)
		im = im.Now(now)
	}
	return im
}

// TargetSet is the node's repository at the target, and whether the target keeps it from whoever holds its credentials.
type TargetSet struct {
	Repository   string              `json:"repository"`
	Immutability backup.Immutability `json:"immutability"`
}

// BackupSpace is how full the target is: for a disk, its filesystem; for SFTP, S3 and B2, the node's repository (as
// restic counts it) out of the quota the admin gave, SizeBytes 0 when none.
type BackupSpace struct {
	UsedBytes uint64 `json:"usedBytes"`
	SizeBytes uint64 `json:"sizeBytes"`
}

// getBackupSpace says how full the node's target is.
func (a *Agent) getBackupSpace(w http.ResponseWriter, r *http.Request) {
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	if st.Target.Kind == backup.Disk {
		fs, ok := a.cfg.Sysroot.(node.StatFS)
		if !ok {
			fail(w, http.StatusServiceUnavailable, "no-space")
			return
		}
		sp, err := fs.Statfs(strings.TrimPrefix(st.Target.Path, "/"))
		if err != nil || sp.FreeBytes > sp.SizeBytes {
			a.log.Error("the disk target's space", "path", st.Target.Path, "err", err)
			fail(w, http.StatusServiceUnavailable, "no-space")
			return
		}
		writeJSON(w, http.StatusOK, BackupSpace{UsedBytes: sp.SizeBytes - sp.FreeBytes, SizeBytes: sp.SizeBytes})
		return
	}
	if st.Target.QuotaBytes == 0 { // nothing to measure against: restic's count, a walk of every snapshot, is skipped
		writeJSON(w, http.StatusOK, BackupSpace{})
		return
	}
	// Never while the target's credentials folder is changed.
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	used, err := a.repo(*st, a.backupDir()).Used(r.Context())
	if err != nil {
		a.log.Error("the repository's use at the target", "err", err)
		fail(w, http.StatusBadGateway, "failed")
		return
	}
	writeJSON(w, http.StatusOK, BackupSpace{UsedBytes: used, SizeBytes: st.Target.QuotaBytes})
}

func (a *Agent) internal(w http.ResponseWriter, what string, err error) {
	a.log.Error(what, "err", err)
	fail(w, http.StatusInternalServerError, "internal")
}

// backupTarget is the node's target, or nil.
func (a *Agent) backupTarget() *backupState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Backup == nil {
		return nil
	}
	st := *a.state.Backup
	return &st
}

// postBackups backs up every app's data, as its plan said, and the sign-in's database, one item at a time.
func (a *Agent) postBackups(w http.ResponseWriter, r *http.Request) {
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	ctx := r.Context()
	a.mu.Lock()
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range a.state.Apps {
			if !yield(id) {
				return
			}
		}
	})
	apps := map[string]appunit.App{}
	data := map[string][]AppData{}
	for _, id := range ids {
		apps[id], data[id] = a.appUnit(id, a.state.Apps[id]), slices.Clone(a.state.Apps[id].Backups)
	}
	signIn := a.state.SignIn != nil
	a.mu.Unlock()

	a.unlock(ctx, *st)
	out := Backups{Results: []BackupResult{}}
	for _, id := range ids {
		m := &appMounts{}
		at := a.cfg.Now() // each app's items are one point to restore it to
		for _, d := range data[id] {
			out.Results = append(out.Results, a.backUpData(ctx, *st, id, apps[id], d, m, at))
		}
	}
	if signIn {
		it := backup.Item{Tags: []string{"platform=sign-in", "data=postgresql:authentik.sql"}, Command: signInDump, File: "authentik.sql"}
		out.Results = append(out.Results, a.backUp(ctx, *st, it, BackupResult{Platform: "sign-in", Data: "postgresql:authentik.sql"}))
	}
	writeJSON(w, http.StatusOK, out)
}

// appMounts are an app's volumes as the engine has them, asked once for a run's items.
type appMounts struct {
	asked  bool
	mounts map[string][]engine.Mount
	err    error
}

func (a *Agent) mountsOf(ctx context.Context, id string, m *appMounts) {
	if m.asked {
		return
	}
	asked, cancel := context.WithTimeout(ctx, engineWait)
	defer cancel()
	m.mounts, m.err = a.cfg.Engine.Mounts(asked, appunit.Project(id))
	m.asked = true
}

// backUpData backs up one thing of an app's, as of at, as its plan said: a folder, where its volume is on the machine, or a dump,
// run in its service through the app's own compose command.
func (a *Agent) backUpData(ctx context.Context, st backupState, id string, u appunit.App, d AppData, m *appMounts, at time.Time) BackupResult {
	res := BackupResult{App: id, Data: d.key()}
	it := backup.Item{Time: at, Tags: []string{"app=" + id, "data=" + res.Data}}
	if d.Path != "" {
		a.mountsOf(ctx, id, m)
		src, why := a.onMachine(id, m.mounts, m.err, d)
		if why != "" {
			res.Error = why
			a.log.Error("backing up an app's folder", "app", id, "data", res.Data, "why", why, "err", m.err)
			return res
		}
		it.Paths = []string{src}
	} else {
		it.Command, it.File = slices.Concat(u.Compose(), []string{"exec", "-T", d.Service}, d.Dump), d.File
	}
	return a.backUp(ctx, st, it, res)
}

// DockerVolumes is where Docker keeps its local volumes.
const DockerVolumes = "/var/lib/docker/volumes"

// onMachine is where a declared folder is on the machine: exactly the root of one of the app's own volumes, as the
// engine has it now (#205's reviews):
//   - mounted at the declared path itself, never a folder inside one, whose components the app could make links that
//     restic, as root, would follow out of the volume;
//   - the app's own (named homerslab-<app>_…), with the local driver, at the engine's own <volumes>/<name>/_data: never
//     another app's or the sign-in's, never a volume whose options bind a folder of the machine;
//   - reached from the volumes folder through no link and across no mount (openat2: RESOLVE_NO_SYMLINKS, NO_XDEV).
//
// What is inside the volume, links included, restic stores as it is and never follows.
func (a *Agent) onMachine(id string, mounts map[string][]engine.Mount, err error, d AppData) (string, string) {
	if err != nil {
		return "", "engine-unreachable"
	}
	ms, ok := mounts[d.Service]
	if !ok {
		return "", "no-container"
	}
	volumes := a.cfg.VolumesDir
	if volumes == "" {
		volumes = DockerVolumes
	}
	for _, m := range ms {
		if m.Destination != d.Path {
			continue
		}
		name, ok := strings.CutPrefix(m.Name, appunit.Project(id)+"_")
		if !m.Volume || !ok || !volumeName.MatchString(name) || m.Driver != "local" || m.Source != path.Join(volumes, m.Name, "_data") {
			return "", "not-its-own-volume"
		}
		if err := noLinks(a.cfg.Root, volumes, path.Join(m.Name, "_data")); err != nil {
			a.log.Error("a volume's folder on the machine", "path", m.Source, "err", err)
			return "", "a-link-in-its-path"
		}
		return m.Source, ""
	}
	return "", "not-a-volume"
}

var volumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// noLinks says whether volumes/rel, under root, is a folder reached through no link, and rel across no mount.
func noLinks(root, volumes, rel string) error {
	r, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(r)
	how := func(xdev uint64) *unix.OpenHow {
		return &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_BENEATH | xdev}
	}
	v, err := unix.Openat2(r, cmp.Or(strings.TrimPrefix(volumes, "/"), "."), how(0))
	if err != nil {
		return err
	}
	defer unix.Close(v)
	fd, err := unix.Openat2(v, rel, how(unix.RESOLVE_NO_XDEV))
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// backUp runs one item and says how it went, in a word.
func (a *Agent) backUp(ctx context.Context, st backupState, it backup.Item, res BackupResult) BackupResult {
	id, err := a.repo(st, a.backupDir()).Backup(ctx, it)
	switch {
	case err == nil:
		res.Snapshot = id
		return res
	case errors.Is(err, backup.ErrIncomplete):
		res.Snapshot, res.Error = id, "incomplete"
		a.incomplete(id)
	case errors.Is(err, backup.ErrLocked):
		res.Error = "locked"
	case errors.Is(err, backup.ErrWrongKey):
		res.Error = "wrong-key"
	case errors.Is(err, backup.ErrNoRepository):
		res.Error = "no-repository"
	default:
		res.Error = "failed"
	}
	a.log.Error("a backup", "app", res.App, "platform", res.Platform, "data", res.Data, "err", err)
	return res
}

// maxManagerState is the largest manager's database the agent takes.
const maxManagerState = 256 << 20

// putManagerBackup backs up the manager's database, as the manager hands over a consistent copy of it.
func (a *Agent) putManagerBackup(w http.ResponseWriter, r *http.Request) {
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManagerState))
	if err != nil || len(b) == 0 {
		fail(w, http.StatusBadRequest, "no-database")
		return
	}
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	a.unlock(r.Context(), *st)
	it := backup.Item{Tags: []string{"platform=manager", "data=manager:manager.db"}, Stdin: b, File: "manager.db"}
	writeJSON(w, http.StatusOK, a.backUp(r.Context(), *st, it, BackupResult{Platform: "manager", Data: "manager:manager.db"}))
}

// postPrune forgets the snapshots the schedule no longer keeps, and frees their data.
func (a *Agent) postPrune(w http.ResponseWriter, r *http.Request) {
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	a.unlock(r.Context(), *st)
	if err := a.repo(*st, a.backupDir()).Prune(r.Context(), backup.DefaultRetention); err != nil {
		a.log.Error("pruning the node's repository", "err", err)
		code := "failed"
		if errors.Is(err, backup.ErrLocked) {
			code = "locked"
		}
		fail(w, http.StatusBadGateway, code)
		return
	}
	// Once a day, after prune: the bucket's lock read again, and every object locked again when that is due.
	im := a.lockRun(r.Context(), *st, a.backupDir())
	a.mu.Lock()
	if a.state.Backup != nil && a.state.Backup.Target == st.Target {
		a.state.Backup.Immutability = im
		if err := a.save(); err != nil {
			a.log.Error("saving the agent's state", "err", err)
		}
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, Pruned{Immutability: im})
}

// Pruned is what a prune says: whether the target keeps the node's backups, as its lock run read it after.
type Pruned struct {
	Immutability backup.Immutability `json:"immutability"`
}
