package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/node"
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
}

// backupState is the node's target and repository, as the agent keeps them in agent.json. None of it is secret.
type backupState struct {
	Node   string        `json:"node"`
	Target backup.Target `json:"target"`
	// Files are the credential IDs sealed for it, in the backup folder.
	Files []string `json:"files"`
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
		if !serviceName.MatchString(d.Service) || folder == dump {
			return false
		}
	}
	return true
}

// The sign-in's database, as the package's compose file runs it.
var signInDump = []string{"/usr/bin/docker", "compose", "--project-name", "homerslab-authentik", "--file", "/usr/share/homerslab/sign-in/authentik.compose.yaml",
	"exec", "-T", "postgresql", "pg_dump", "--username=authentik", "--clean", "--if-exists", "authentik"}

// backupCache is restic's cache, a state folder of the unit's on the app-data disk, counted in that disk's use.
var backupCache = filepath.Join(node.DataPath, "backup-cache")

func (a *Agent) backupDir() string { return filepath.Join(a.cfg.StateDir, "backup") }

// The longest the agent waits for one restic run: a first backup of a photo library takes hours.
const backupWait = 12 * time.Hour

// repo is the node's repository as one run of restic in a unit of its own sees it, with the credentials sealed in dir.
func (a *Agent) repo(st backupState, dir string) backup.Repo {
	restic := a.cfg.Restic
	if restic == "" {
		restic = backup.Restic
	}
	// The credentials folder restic is told of is the one systemd makes for the unit: each run gets a unit of its own,
	// named as it starts, and its own folder's name in place of this placeholder.
	const dirOf = "/run/credentials/homerslab-backup.service"
	r := backup.Repo{Restic: restic, Target: st.Target, Node: st.Node, Dir: dirOf, Cache: backupCache}
	r.Run = func(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
		unit := fmt.Sprintf("homerslab-backup-%d-%d", a.cfg.Now().UnixNano(), a.backupRuns.Add(1))
		here := func(s string) string { return strings.ReplaceAll(s, dirOf, "/run/credentials/"+unit+".service") }
		args := []string{"--unit=" + unit, "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
			"-p", "StateDirectory=" + strings.TrimPrefix(backupCache, "/var/lib/"), "-p", "ProtectSystem=strict", "-p", "PrivateTmp=yes",
			"-p", "NoNewPrivileges=yes", "-p", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH"}
		if st.Target.Kind == backup.Disk {
			args = append(args, "-p", "ReadWritePaths="+st.Target.Path)
		}
		for _, f := range st.Files {
			args = append(args, "-p", "LoadCredentialEncrypted="+f+":"+filepath.Join(dir, f+".cred"))
		}
		for _, e := range env {
			args = append(args, "-E", here(e))
		}
		args = append(args, "--")
		for _, v := range argv {
			args = append(args, here(v))
		}
		ctx, cancel := context.WithTimeout(ctx, backupWait)
		defer cancel()
		out, err := a.cfg.Run(ctx, host.Cmd{Name: "systemd-run", Args: args, Stdin: stdin})
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
	if err := a.repo(st, next).Init(ctx); err != nil {
		a.log.Error("making the node's repository at the target", "err", err)
		_ = os.RemoveAll(next)
		if errors.Is(err, backup.ErrWrongKey) {
			fail(w, http.StatusConflict, "wrong-key")
			return
		}
		fail(w, http.StatusBadGateway, "target-failed")
		return
	}
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
	a.log.Info("the node's backups go to the household's target", "repository", loc)
	writeJSON(w, http.StatusOK, map[string]string{"repository": loc})
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

	out := Backups{Results: []BackupResult{}}
	for _, id := range ids {
		var mounts map[string][]engine.Mount
		var mountsErr error
		for _, d := range data[id] {
			res := BackupResult{App: id, Data: d.Service + ":" + d.Path}
			it := backup.Item{Tags: []string{"app=" + id}}
			if d.Path != "" {
				if mounts == nil && mountsErr == nil {
					asked, cancel := context.WithTimeout(ctx, engineWait)
					mounts, mountsErr = a.cfg.Engine.Mounts(asked, appunit.Project(id))
					cancel()
				}
				src, why := onMachine(mounts, mountsErr, d)
				if why != "" {
					res.Error = why
					a.log.Error("backing up an app's folder", "app", id, "data", res.Data, "why", why, "err", mountsErr)
					out.Results = append(out.Results, res)
					continue
				}
				it.Paths = []string{src}
			} else {
				res.Data = d.Service + ":" + d.File
				it.Command, it.File = slices.Concat(apps[id].Compose(), []string{"exec", "-T", d.Service}, d.Dump), d.File
			}
			it.Tags = append(it.Tags, "data="+res.Data)
			out.Results = append(out.Results, a.backUp(ctx, *st, it, res))
		}
	}
	if signIn {
		it := backup.Item{Tags: []string{"platform=sign-in", "data=postgresql:authentik.sql"}, Command: signInDump, File: "authentik.sql"}
		out.Results = append(out.Results, a.backUp(ctx, *st, it, BackupResult{Platform: "sign-in", Data: "postgresql:authentik.sql"}))
	}
	writeJSON(w, http.StatusOK, out)
}

// onMachine is where a declared folder is on the machine: under the service's volume or folder that holds it. A
// folder that is in no volume (in the container's own layer) can't be backed up, and says so.
func onMachine(mounts map[string][]engine.Mount, err error, d AppData) (string, string) {
	if err != nil {
		return "", "engine-unreachable"
	}
	ms, ok := mounts[d.Service]
	if !ok {
		return "", "no-container"
	}
	best := -1
	for i, m := range ms {
		if (m.Destination == d.Path || strings.HasPrefix(d.Path, strings.TrimSuffix(m.Destination, "/")+"/")) && (best < 0 || len(m.Destination) > len(ms[best].Destination)) {
			best = i
		}
	}
	if best < 0 {
		return "", "not-in-a-volume"
	}
	src := strings.TrimSuffix(ms[best].Source, "/") + strings.TrimPrefix(d.Path, strings.TrimSuffix(ms[best].Destination, "/"))
	if !backup.ValidPath(src) {
		return "", "not-in-a-volume"
	}
	return src, ""
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
	if err := a.repo(*st, a.backupDir()).Prune(r.Context()); err != nil {
		a.log.Error("pruning the node's repository", "err", err)
		code := "failed"
		if errors.Is(err, backup.ErrLocked) {
			code = "locked"
		}
		fail(w, http.StatusBadGateway, code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
