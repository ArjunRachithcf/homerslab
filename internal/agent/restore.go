package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/appunit"
	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/engine"
	"github.com/ArjunRachithcf/homeward/internal/host"
)

// Restoring an app to an earlier point (#22; spec: Updates, removal, eject). The manager hands the agent the snapshots
// its restore plan named, one for each thing of the app's that is backed up; the agent:
//   - takes only snapshots of this app's own data, as its install plan declared it, found in the node's repository
//     under the app's own tags;
//   - backs up the app as it is now first (the snapshot a failed restore rolls back to), and changes nothing unless
//     every one of those backups worked;
//   - stops the app, and makes each folder exactly what its snapshot held: the app's own volume's root, found and
//     checked as for a backup (onMachine: the app's own named volume, through no link and across no mount), written
//     by a run of restic that can write nothing else on the machine. A link in a snapshot is put back as a link;
//   - runs only the dumps' services, as the app's unit runs them, hands each dump to the app's own restore command in
//     its service, and then starts the app on what it restored;
//   - waits for the app's health checks, and on any failure does the same with the snapshots it took first.
//
// Everything is standard restic: a restore by hand is `restic restore <snapshot>:<folder> --target <folder> --delete`
// and `restic dump <snapshot> /<file>`, as the recovery kit's how-to says.

// AppSnapshot is one backup of one thing of an app's.
type AppSnapshot struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Data string    `json:"data"`
	Size uint64    `json:"size"`
	// Incomplete: restic saved it without some of its files. It is never restored.
	Incomplete bool `json:"incomplete,omitempty"`
}

// AppBackups are an app's backups in the node's repository, oldest first.
type AppBackups struct {
	Snapshots []AppSnapshot `json:"snapshots"`
}

// RestoreItem is one thing of an app's and the snapshot to restore it from.
type RestoreItem struct {
	Data     string `json:"data"`
	Snapshot string `json:"snapshot"`
}

// RestoreRequest is a restore plan's snapshots.
type RestoreRequest struct {
	Items []RestoreItem `json:"items"`
}

// RestoreResult is how a restore went: restored, rolled-back (the app is as it was, and Error says what failed),
// rollback-failed, or unchanged (the app didn't stop, so nothing was written); Before are the snapshots of the app as it was, taken first.
type RestoreResult struct {
	State  string        `json:"state"`
	Error  string        `json:"error,omitempty"`
	Before []RestoreItem `json:"before"`
}

// How long a restore takes at most, and how long the agent waits for the app to be healthy after one.
const (
	// RestoreDeadline is how long a restore takes at most: the try, and its rollback in a time of its own.
	RestoreDeadline = 2*restoreWait + 10*time.Minute
	restoreWait     = 3*backupWait + time.Hour
	healthWait      = 5 * time.Minute
)

// getAppBackups lists an app's backups: the node's snapshots that carry the app's tag.
func (a *Agent) getAppBackups(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !appunit.ID.MatchString(id) || reservedApps[id] {
		fail(w, http.StatusBadRequest, "bad-app")
		return
	}
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	snaps, err := a.repo(*st, a.backupDir()).Snapshots(r.Context(), "app="+id)
	if err != nil {
		a.log.Error("listing an app's backups", "app", id, "err", err)
		fail(w, http.StatusBadGateway, "target-failed")
		return
	}
	out := AppBackups{Snapshots: []AppSnapshot{}}
	for _, s := range snaps {
		for _, t := range s.Tags {
			if data, ok := strings.CutPrefix(t, "data="); ok {
				out.Snapshots = append(out.Snapshots, AppSnapshot{ID: s.ID, Time: s.Time, Data: data, Size: s.Size(),
					Incomplete: slices.Contains(st.Incomplete, s.ID)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// restoring is one thing being restored: the app's data, the snapshot, the folder the snapshot holds (a folder's).
type restoring struct {
	data     AppData
	snapshot string
	from     string
}

func (a *Agent) postRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req RestoreRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	st := a.backupTarget()
	if st == nil {
		fail(w, http.StatusConflict, "no-target")
		return
	}
	a.mu.Lock()
	s := a.state.Apps[id]
	var u appunit.App
	var declared []AppData
	if s != nil {
		u, declared = a.appUnit(id, s), slices.Clone(s.Backups)
	}
	a.mu.Unlock()
	if s == nil {
		fail(w, http.StatusNotFound, "no-such-app")
		return
	}
	// Only what the app's install plan backs up, each once, from a snapshot named by its id: never a path, a command or
	// a file name of the request's own.
	var items []restoring
	for _, it := range req.Items {
		at := slices.IndexFunc(declared, func(d AppData) bool { return d.key() == it.Data })
		switch {
		case !backup.ValidSnapshot(it.Snapshot) || len(it.Snapshot) != 64:
			fail(w, http.StatusBadRequest, "bad-snapshot")
			return
		case at < 0 || slices.ContainsFunc(items, func(o restoring) bool { return o.data.key() == it.Data }):
			fail(w, http.StatusBadRequest, "not-its-data")
			return
		case declared[at].Path == "" && !backup.ValidCommand(declared[at].Restore):
			fail(w, http.StatusUnprocessableEntity, "no-restore-command")
			return
		}
		items = append(items, restoring{data: declared[at], snapshot: it.Snapshot})
	}
	if len(items) == 0 {
		fail(w, http.StatusBadRequest, "nothing-to-restore")
		return
	}
	if !a.backingUp.TryLock() {
		fail(w, http.StatusConflict, "backups-running")
		return
	}
	defer a.backingUp.Unlock()
	// No install of the app while it is restored: one under way finishes first, and one asked for meanwhile is
	// refused (putApp), never run on a half-restored app.
	a.installs.Lock()
	a.mu.Lock()
	if a.state.Apps[id] == nil || a.state.Apps[id].Plan != s.Plan {
		a.mu.Unlock()
		a.installs.Unlock()
		fail(w, http.StatusConflict, "app-changed")
		return
	}
	if a.restoringApps == nil {
		a.restoringApps = map[string]bool{}
	}
	a.restoringApps[id] = true
	a.mu.Unlock()
	a.installs.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.restoringApps, id)
		// A sign-in that changed meanwhile: the app follows it now (its compose file, and a restart if it changed).
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
		defer cancel()
		a.appsFollowSignIn(ctx)
	}()
	// From here the restore runs to its end whatever the manager does: a request given up on never leaves an app
	// stopped or half restored.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), restoreWait)
	defer cancel()
	a.unlock(ctx, *st)

	// Each snapshot must be one of this app's own data: found under the app's and the data's tags, never another
	// app's or the platform's, whatever the request says.
	snaps, err := a.repo(*st, a.backupDir()).Snapshots(ctx, "app="+id)
	if err != nil {
		a.log.Error("finding a restore's snapshots", "app", id, "err", err)
		fail(w, http.StatusBadGateway, "target-failed")
		return
	}
	for i, it := range items {
		at := slices.IndexFunc(snaps, func(s backup.Snapshot) bool {
			return s.ID == it.snapshot && slices.Contains(s.Tags, "app="+id) && slices.Contains(s.Tags, "data="+it.data.key())
		})
		if at < 0 {
			fail(w, http.StatusConflict, "no-such-snapshot")
			return
		}
		if slices.Contains(st.Incomplete, it.snapshot) {
			fail(w, http.StatusConflict, "incomplete-snapshot")
			return
		}
		if it.data.Path != "" {
			if len(snaps[at].Paths) != 1 || !backup.ValidPath(snaps[at].Paths[0]) {
				fail(w, http.StatusConflict, "no-such-snapshot")
				return
			}
			items[i].from = snaps[at].Paths[0]
		}
	}
	// The app's volumes, while its containers are there to ask: a folder that isn't the app's own volume's root now
	// is refused before anything changes.
	m := &appMounts{}
	for _, it := range items {
		if it.data.Path == "" {
			continue
		}
		a.mountsOf(ctx, id, m)
		if _, why := a.onMachine(id, m.mounts, m.err, it.data); why != "" {
			a.log.Error("a restore's folder", "app", id, "data", it.data.key(), "why", why, "err", m.err)
			fail(w, http.StatusConflict, why)
			return
		}
	}

	// A snapshot of the app as it is, first: without every one of them, nothing is restored.
	out := RestoreResult{Before: []RestoreItem{}}
	var before []restoring
	now := a.cfg.Now()
	for _, it := range items {
		res := a.backUpData(ctx, *st, id, u, it.data, m, now)
		if res.Error != "" || !backup.ValidSnapshot(res.Snapshot) { // an incomplete one too: it couldn't be put back
			a.log.Error("the snapshot before a restore; nothing was restored", "app", id, "data", res.Data, "why", res.Error)
			fail(w, http.StatusBadGateway, "snapshot-failed")
			return
		}
		b := restoring{data: it.data, snapshot: res.Snapshot}
		if it.data.Path != "" {
			b.from, _ = a.onMachine(id, m.mounts, m.err, it.data)
		}
		before = append(before, b)
		out.Before = append(out.Before, RestoreItem{Data: res.Data, Snapshot: res.Snapshot})
	}

	a.log.Info("restoring an app to an earlier point", "app", id, "items", len(items))
	if why := a.restoreTo(ctx, *st, id, u, items, m); why == errNotStopped {
		out.State, out.Error = "unchanged", why
		writeJSON(w, http.StatusOK, out)
		return
	} else if why != "" {
		a.log.Error("a restore failed: rolling the app back to the snapshot taken first", "app", id, "why", why)
		out.State, out.Error = "rolled-back", why
		// The rollback has a time of its own, whatever the failed try took.
		back, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreWait)
		again := a.restoreTo(back, *st, id, u, before, m)
		cancel()
		if again != "" {
			a.log.Error("rolling a failed restore back", "app", id, "why", again)
			out.State = "rollback-failed"
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.State = "restored"
	a.log.Info("an app is restored to an earlier point, and healthy", "app", id)
	writeJSON(w, http.StatusOK, out)
}

// errNotStopped is an app that didn't stop for its restore: nothing of it was changed.
const errNotStopped = "stop-failed"

// restoreTo makes the app what items' snapshots held, and says in a word what failed, or "". The app's own unit is
// stopped throughout: its folders are put back, then its dumps' services alone run for its restore commands, and only
// then is the app started, on what was put back.
func (a *Agent) restoreTo(ctx context.Context, st backupState, id string, u appunit.App, items []restoring, m *appMounts) string {
	unit := appunit.Unit(id)
	if err := a.step(ctx, "systemctl", "stop", unit); err != nil {
		// Nothing was written: the app is started again as it was, and there is nothing to roll back.
		a.log.Error("stopping an app for its restore", "app", id, "err", err)
		if err := a.step(ctx, "systemctl", "start", unit); err != nil {
			a.log.Error("starting an app again after it didn't stop", "app", id, "err", err)
		}
		return errNotStopped
	}
	failed := ""
	var dumps []restoring
	for _, it := range items {
		if it.data.Path == "" {
			dumps = append(dumps, it)
			continue
		}
		// The volume's folder is found and checked again right before restic writes it: the app's own, through no link.
		into, why := a.onMachine(id, m.mounts, m.err, it.data)
		if why != "" {
			failed = why
			break
		}
		if err := a.repoWriting(st, a.backupDir(), writes{into: into}).Restore(ctx, it.snapshot, it.from, into); err != nil {
			a.log.Error("restoring an app's folder", "app", id, "data", it.data.key(), "err", err)
			failed = "restore-failed"
			break
		}
	}
	if failed == "" && len(dumps) > 0 {
		failed = a.restoreDumps(ctx, st, id, u, dumps)
	}
	// The app is started again whatever happened: a rollback, or the admin, finds it running.
	if err := a.step(ctx, "systemctl", "start", unit); err != nil {
		a.log.Error("starting an app after its restore", "app", id, "err", err)
		return cmp.Or(failed, "start-failed")
	}
	if failed != "" {
		return failed
	}
	if !a.waitFor(ctx, id, func(s AppStatus) bool { return s.Health == "healthy" }) {
		return "unhealthy"
	}
	return ""
}

// step runs one of a restore's commands on systemd, in a minute of its own: whatever time the restore has left, the
// app is never left stopped because it ran out.
func (a *Agent) step(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	return a.run(ctx, name, args...)
}

// restoreDumps runs the dumps' services alone, as the app's unit runs them (its secrets, its compose command), and
// hands each dump to the app's own restore command in its service.
func (a *Agent) restoreDumps(ctx context.Context, st backupState, id string, u appunit.App, dumps []restoring) string {
	var services []string
	for _, it := range dumps {
		if !slices.Contains(services, it.data.Service) {
			services = append(services, it.data.Service)
		}
	}
	unit := fmt.Sprintf("homeward-restore-%s-%d", id, a.backupRuns.Add(1))
	if _, err := a.cfg.Run(ctx, host.Cmd{Name: "systemd-run", Args: u.ServicesRun(unit, services)}); err != nil {
		a.log.Error("running an app's databases for its restore", "app", id, "err", err)
		return "service-not-running"
	}
	defer func() {
		if err := a.step(ctx, "systemctl", "stop", unit+".service"); err != nil {
			a.log.Error("stopping an app's databases after its restore", "app", id, "err", err)
		}
	}()
	for _, it := range dumps {
		svc := it.data.Service
		running := func(s AppStatus) bool {
			return slices.ContainsFunc(s.Containers, func(c engine.Container) bool {
				return c.Service == svc && c.State == "running" && c.Health != "starting" && c.Health != "unhealthy"
			})
		}
		if !a.waitFor(ctx, id, running) {
			return "service-not-running"
		}
		if why := a.restoreDump(ctx, st, id, u, it); why != "" {
			return why
		}
	}
	return ""
}

// restoreAttempts is how often a dump is handed to the app's restore command: a database without a health check of
// its own may still be starting when its container runs.
const restoreAttempts = 3

// restoreDump writes one dump to a file of root's and hands it to the app's restore command, then removes it.
func (a *Agent) restoreDump(ctx context.Context, st backupState, id string, u appunit.App, it restoring) string {
	file := filepath.Join(restoreDir, id+"."+it.data.File)
	// The dump is a copy of the app's database in the clear: removed whatever happens, by a unit that may (the agent's
	// own can't write the app-data disk).
	defer func() {
		remove := fmt.Sprintf("homeward-restore-%s-%d", id, a.backupRuns.Add(1))
		if err := a.step(ctx, "systemd-run", "--unit="+remove, "--quiet", "--wait", "--collect", "--", "/usr/bin/rm", "-f", "--", file); err != nil {
			a.log.Error("removing a restored dump's file", "file", file, "err", err)
		}
	}()
	if err := a.repoWriting(st, a.backupDir(), writes{dumps: true}).Dump(ctx, it.snapshot, it.data.File, file); err != nil {
		a.log.Error("writing an app's dump for its restore", "app", id, "data", it.data.key(), "err", err)
		return "dump-restore-failed"
	}
	gap := min(10*time.Second, a.cfg.HealthWait/5)
	var err error
	for try := range restoreAttempts {
		if try > 0 {
			select {
			case <-ctx.Done():
				return "dump-restore-failed"
			case <-time.After(gap):
			}
		}
		// The app's own command reads the dump as its standard input, in a unit of its own: the agent never holds an
		// app's database in memory, and the command is the install plan's, of plain arguments.
		unit := fmt.Sprintf("homeward-restore-%s-%d", id, a.backupRuns.Add(1))
		args := []string{"--unit=" + unit, "--quiet", "--wait", "--collect", "--service-type=exec",
			"-p", "StandardInput=file:" + file, "-p", "NoNewPrivileges=yes", "--"}
		args = append(args, slices.Concat(u.Compose(), []string{"exec", "-T", it.data.Service}, it.data.Restore)...)
		run, cancel := context.WithTimeout(ctx, backupWait)
		_, err = a.cfg.Run(run, host.Cmd{Name: "systemd-run", Args: args})
		if run.Err() != nil { // only systemd-run was ended: its unit is stopped too
			_ = a.step(ctx, "systemctl", "stop", unit+".service")
		}
		cancel()
		if err == nil {
			return ""
		}
		a.log.Warn("an app's restore command failed", "app", id, "data", it.data.key(), "try", try+1, "err", err)
	}
	return "dump-restore-failed"
}

// waitFor waits until the app is as ok says, for as long as a restore waits for health.
func (a *Agent) waitFor(ctx context.Context, id string, ok func(AppStatus) bool) bool {
	wait := a.cfg.HealthWait
	every := min(2*time.Second, wait/10)
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		for _, s := range a.appsStatus(ctx).Apps {
			if s.ID == id && ok(s) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}
