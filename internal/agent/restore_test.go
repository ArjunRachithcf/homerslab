package agent_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/agent"
	"github.com/ArjunRachithcf/homeward/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homeward/internal/engine"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

const (
	libraryVolume = "/var/lib/docker/volumes/homerslab-ledger_library/_data"
	appUnit       = "homeward-app-ledger.service"
)

var yesterday = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

// restorable is backingUp, with yesterday's backup of the ledger app in the node's repository: its library and its
// database's dump. It returns the two snapshots' ids.
func restorable(t *testing.T, c agent.Config) (sock string, h *agenttest.Host, library, dump string) {
	t.Helper()
	sock, h, _ = signedIn(t, c)
	install(t, sock, "ledger", withBackups())
	h.Mount("homerslab-ledger", "web", "/data/library", libraryVolume)
	h.Mount("homerslab-ledger", "web", "/cache", "/var/lib/docker/volumes/homerslab-ledger_cache/_data")
	if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK {
		t.Fatalf("the target: %d %s", code, body)
	}
	library = h.AddSnapshot(yesterday, libraryVolume, 4096, "app=ledger", "data=web:/data/library")
	dump = h.AddSnapshot(yesterday.Add(time.Minute), "/ledger.sql", 512, "app=ledger", "data=db:ledger.sql")
	return sock, h, library, dump
}

func restore(t *testing.T, sock string, items ...agent.RestoreItem) (int, agent.RestoreResult, string) {
	t.Helper()
	code, body := call(t, sock, "POST", "/v1/apps/ledger/restore", agent.RestoreRequest{Items: items})
	var res agent.RestoreResult
	_ = json.Unmarshal(body, &res)
	return code, res, string(body)
}

// steps are what a restore did to the machine, in order: the app's unit stopped and started, and restic's runs.
func steps(h *agenttest.Host, from int) []string {
	var out []string
	for _, c := range h.Cmds()[from:] {
		line := c.String()
		switch {
		case c.Name == "systemctl" && strings.HasSuffix(line, " "+appUnit) && !strings.Contains(line, "is-active"):
			out = append(out, c.Args[len(c.Args)-2])
		case c.Name == "systemd-run":
			argv := c.Args[slices.Index(c.Args, "--")+1:]
			for _, sub := range []string{"backup", "restore", "dump", "exec"} {
				if slices.Contains(argv, sub) && (sub != "exec" || argv[0] == "/usr/bin/docker") {
					out = append(out, sub)
					break
				}
			}
		}
	}
	return out
}

// An app's backups are the node's snapshots that carry its tag, each with its time and size: never another app's, or
// the platform's own.
func TestAnAppsBackupsAreListedWithTheirTimeAndSize(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, dump := restorable(t, agent.Config{})
	h.AddSnapshot(yesterday, "/var/lib/docker/volumes/homerslab-immich_library/_data", 1<<30, "app=immich", "data=server:/data")
	h.AddSnapshot(yesterday, "/authentik.sql", 1<<20, "platform=sign-in", "data=postgresql:authentik.sql")
	code, body := call(t, sock, "GET", "/v1/apps/ledger/backups", nil)
	var got agent.AppBackups
	if err := json.Unmarshal(body, &got); err != nil || code != http.StatusOK {
		t.Fatalf("the app's backups: %d %s", code, body)
	}
	want := []agent.AppSnapshot{{ID: library, Time: yesterday, Data: "web:/data/library", Size: 4096},
		{ID: dump, Time: yesterday.Add(time.Minute), Data: "db:ledger.sql", Size: 512}}
	if !slices.Equal(got.Snapshots, want) {
		t.Errorf("the app's backups:\n got %+v\nwant %+v", got.Snapshots, want)
	}
	if code, _ := call(t, sock, "GET", "/v1/apps/authentik/backups", nil); code != http.StatusBadRequest {
		t.Errorf("the sign-in's backups, asked for as an app's: %d", code)
	}
}

// A restore backs the app up as it is first, stops it, makes its own volume exactly what the snapshot held (a run of
// restic that may write that one folder and nothing else of the machine), starts it, hands the dump to the app's own
// restore command in its service, restarts it and waits for its health.
func TestARestoreSnapshotsFirstThenPutsBackExactlyTheAppsOwnData(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, dump := restorable(t, agent.Config{})
	from, runs := len(h.Cmds()), len(h.Restic())
	code, res, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}, agent.RestoreItem{Data: "db:ledger.sql", Snapshot: dump})
	if code != http.StatusOK || res.State != "restored" || res.Error != "" {
		t.Fatalf("the restore: %d %s", code, body)
	}
	if got := steps(h, from); !slices.Equal(got, []string{"backup", "backup", "stop", "restore", "dump", "exec", "start"}) {
		t.Errorf("the restore's steps: %q", got)
	}
	if len(res.Before) != 2 || res.Before[0].Data != "web:/data/library" || res.Before[0].Snapshot == library || len(res.Before[0].Snapshot) != 64 ||
		res.Before[1].Data != "db:ledger.sql" {
		t.Errorf("the snapshots taken first: %+v", res.Before)
	}
	if got := h.Restored(); !slices.Equal(got, []string{
		"restore " + library + ":" + libraryVolume + " " + libraryVolume,
		"run db", // the dump's service alone, the app's own unit still stopped
		"dump " + dump + " /ledger.sql",
		"command db psql --set=ON_ERROR_STOP=1 --username=postgres ledger",
		"remove /var/lib/homeward/restore/ledger.ledger.sql",
	}) {
		t.Errorf("what was put back: %q", got)
	}
	for _, r := range h.Restic()[runs:] {
		unit := strings.Join(r.Unit, " ")
		writes := strings.Contains(unit, "ReadWritePaths=")
		switch {
		case slices.Contains(r.Argv, "restore"):
			// The one run that writes an app's data: its volume's folder, read-only everything else.
			for _, p := range []string{"ReadWritePaths=" + libraryVolume, "ProtectSystem=strict", "PrivateDevices=yes", "ProtectKernelTunables=yes", "NoNewPrivileges=yes"} {
				if !strings.Contains(unit, p) {
					t.Errorf("the restore's unit lacks %s: %s", p, unit)
				}
			}
			if strings.Count(unit, "ReadWritePaths=") != 1 || strings.Contains(unit, "CAP_SYS_ADMIN") || strings.Contains(unit, "CAP_MKNOD") {
				t.Errorf("the restore's unit: %s", unit)
			}
		case writes || strings.Contains(unit, "CAP_DAC_OVERRIDE"):
			t.Errorf("a run that restores no folder may write: %s", unit)
		}
	}
	// The dump reaches the app's command as a file's contents, in a unit: never as an argument.
	var cmd string
	for _, c := range h.Cmds()[from:] {
		if c.Name == "systemd-run" && slices.Contains(c.Args, "psql") {
			cmd = c.String()
		}
	}
	if !strings.Contains(cmd, "StandardInput=file:/var/lib/homeward/restore/ledger.ledger.sql") || !strings.Contains(cmd, "exec -T db psql") {
		t.Errorf("the app's restore command: %s", cmd)
	}
}

// A restore takes only this app's own data from this app's own snapshots, named by their ids: another app's snapshot,
// data its plan doesn't back up, a path that climbs (`..`), or anything but an id, is refused before anything happens.
func TestARestoreTakesOnlyTheAppsOwnSnapshots(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, dump := restorable(t, agent.Config{})
	theirs := h.AddSnapshot(yesterday, "/var/lib/docker/volumes/homerslab-immich_library/_data", 1<<30, "app=immich", "data=web:/data/library")
	signIn := h.AddSnapshot(yesterday, "/authentik.sql", 1<<20, "platform=sign-in", "data=db:ledger.sql")
	etc := h.AddSnapshot(yesterday, "/etc", 1<<20, "app=ledger", "data=web:/etc")
	from := len(h.Cmds())
	for name, c := range map[string]struct {
		item agent.RestoreItem
		code int
		why  string
	}{
		"another app's snapshot":    {agent.RestoreItem{Data: "web:/data/library", Snapshot: theirs}, http.StatusConflict, "no-such-snapshot"},
		"the sign-in's snapshot":    {agent.RestoreItem{Data: "db:ledger.sql", Snapshot: signIn}, http.StatusConflict, "no-such-snapshot"},
		"the dump's, for a folder":  {agent.RestoreItem{Data: "web:/data/library", Snapshot: dump}, http.StatusConflict, "no-such-snapshot"},
		"data that isn't backed up": {agent.RestoreItem{Data: "web:/cache", Snapshot: library}, http.StatusBadRequest, "not-its-data"},
		"a folder of the machine":   {agent.RestoreItem{Data: "web:/etc", Snapshot: etc}, http.StatusBadRequest, "not-its-data"},
		"a path that climbs":        {agent.RestoreItem{Data: "web:/data/library/../../etc", Snapshot: library}, http.StatusBadRequest, "not-its-data"},
		"a name, not an id":         {agent.RestoreItem{Data: "web:/data/library", Snapshot: "latest"}, http.StatusBadRequest, "bad-snapshot"},
		"an id with a path":         {agent.RestoreItem{Data: "web:/data/library", Snapshot: library[:60] + ":/.."}, http.StatusBadRequest, "bad-snapshot"},
		"a flag":                    {agent.RestoreItem{Data: "web:/data/library", Snapshot: "--target=/etc"}, http.StatusBadRequest, "bad-snapshot"},
	} {
		if code, _, body := restore(t, sock, c.item); code != c.code || !strings.Contains(body, c.why) {
			t.Errorf("%s: %d %s, want %d %s", name, code, body, c.code, c.why)
		}
	}
	if code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}); code != http.StatusBadRequest {
		t.Errorf("the same data twice: %d %s", code, body)
	}
	if code, _, body := restore(t, sock); code != http.StatusBadRequest {
		t.Errorf("nothing to restore: %d %s", code, body)
	}
	if code, body := call(t, sock, "POST", "/v1/apps/immich/restore", agent.RestoreRequest{Items: []agent.RestoreItem{{Data: "web:/data/library", Snapshot: theirs}}}); code != http.StatusNotFound {
		t.Errorf("an app that isn't installed: %d %s", code, body)
	}
	if got := steps(h, from); len(got) != 0 || len(h.Restored()) != 0 {
		t.Errorf("a refused restore did %q, and put back %q", got, h.Restored())
	}
}

// restic, as root, writes only into the app's own volume's folder, where the engine made it: a volume reached through
// a link (here, to the machine's /etc), another app's, or one bound elsewhere, is refused before the app is stopped.
func TestARestoreNeverWritesOutsideTheAppsOwnVolume(t *testing.T) {
	testenv.Isolate(t)
	t.Run("a link on the way to the volume", func(t *testing.T) {
		sock, h, _ := signedIn(t, agent.Config{})
		install(t, sock, "ledger", withBackups())
		if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK {
			t.Fatalf("the target: %d %s", code, body)
		}
		volumes := filepath.Join(h.Root, "var/lib/docker/volumes")
		if err := os.MkdirAll(filepath.Join(h.Root, "etc/_data"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(volumes, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../../../etc", filepath.Join(volumes, "homerslab-ledger_library")); err != nil {
			t.Fatal(err)
		}
		h.Mount("homerslab-ledger", "web", "/data/library", libraryVolume)
		library := h.AddSnapshot(yesterday, libraryVolume, 4096, "app=ledger", "data=web:/data/library")
		from := len(h.Cmds())
		if code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}); code != http.StatusConflict || !strings.Contains(body, "a-link-in-its-path") {
			t.Errorf("a volume reached through a link: %d %s", code, body)
		}
		if got := steps(h, from); len(got) != 0 || len(h.Restored()) != 0 {
			t.Errorf("the refused restore did %q", got)
		}
	})
	for name, m := range map[string]engine.Mount{
		"another app's volume": {Name: "homerslab-immich_library", Driver: "local", Source: "/var/lib/docker/volumes/homerslab-immich_library/_data"},
		"bound elsewhere":      {Name: "homerslab-ledger_library", Driver: "local", Source: "/etc"},
		// A storage location is a bind of a folder of the machine (#28), never an app's volume: no restore writes it.
		"a storage location's folder": {Source: "/srv/media/Films"},
	} {
		t.Run(name, func(t *testing.T) {
			sock, h, _ := signedIn(t, agent.Config{})
			install(t, sock, "ledger", withBackups())
			if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK {
				t.Fatalf("the target: %d %s", code, body)
			}
			m.Destination, m.Volume = "/data/library", m.Name != ""
			_ = os.MkdirAll(filepath.Join(h.Root, m.Source), 0o755)
			h.MountAs("homerslab-ledger", "web", m)
			library := h.AddSnapshot(yesterday, libraryVolume, 4096, "app=ledger", "data=web:/data/library")
			from := len(h.Cmds())
			if code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}); code != http.StatusConflict || !strings.Contains(body, "not-its-own-volume") {
				t.Errorf("%d %s", code, body)
			}
			if got := steps(h, from); len(got) != 0 || len(h.Restored()) != 0 {
				t.Errorf("the refused restore did %q", got)
			}
		})
	}
}

// Without a snapshot of the app as it is, nothing is restored: the app is never stopped.
func TestWithoutTheSnapshotFirstNothingIsRestored(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, dump := restorable(t, agent.Config{})
	h.FailRestic("--tag app=ledger --tag data=db:ledger.sql --stdin-from-command", 1)
	from := len(h.Cmds())
	code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}, agent.RestoreItem{Data: "db:ledger.sql", Snapshot: dump})
	if code != http.StatusBadGateway || !strings.Contains(body, "snapshot-failed") {
		t.Errorf("the restore: %d %s", code, body)
	}
	if got := steps(h, from); !slices.Equal(got, []string{"backup", "backup"}) || len(h.Restored()) != 0 {
		t.Errorf("without its snapshot first, the restore did %q and put back %q", got, h.Restored())
	}
}

// A restore that fails (restic's, the app's own restore command, or the app's health afterwards) rolls the app back to
// the snapshot taken first, the same way, and says what failed.
func TestAFailedRestoreRollsBackToTheSnapshotTakenFirst(t *testing.T) {
	testenv.Isolate(t)
	for name, c := range map[string]struct {
		fail  func(h *agenttest.Host, library, upstream string)
		why   string
		state string
		tried []string // what the failed try put back, before the rollback
	}{
		"restic fails": {func(h *agenttest.Host, library, _ string) {
			h.FailRestic("restore --target "+libraryVolume+" --delete -- "+library, 1)
		},
			"restore-failed", "rolled-back", nil},
		"the app's restore command fails": {func(h *agenttest.Host, _, _ string) {
			h.FailRestic("psql --set=ON_ERROR_STOP=1 --username=postgres ledger", 3)
		},
			"dump-restore-failed", "rollback-failed", []string{"restore", "run", "dump", "remove"}},
		"the app's database never runs": {func(h *agenttest.Host, _, _ string) { h.Sick("homerslab-ledger", "unhealthy") },
			"service-not-running", "rollback-failed", []string{"restore", "run"}},
		"the app is unhealthy afterwards": {func(h *agenttest.Host, _, upstream string) { h.Answer(upstream, "Internal Server Error") },
			"unhealthy", "rollback-failed", []string{"restore", "run", "dump", "command", "remove"}},
	} {
		t.Run(name, func(t *testing.T) {
			sock, h, library, dump := restorable(t, agent.Config{HealthWait: 50 * time.Millisecond})
			var apps agent.AppsStatus
			_, listed := call(t, sock, "GET", "/v1/apps", nil)
			if err := json.Unmarshal(listed, &apps); err != nil || len(apps.Apps) != 1 {
				t.Fatalf("the apps: %s", listed)
			}
			c.fail(h, library, apps.Apps[0].Upstream)
			from := len(h.Cmds())
			code, res, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}, agent.RestoreItem{Data: "db:ledger.sql", Snapshot: dump})
			if code != http.StatusOK || res.State != c.state || res.Error != c.why || len(res.Before) != 2 {
				t.Fatalf("the restore: %d %s, want %s %s", code, body, c.state, c.why)
			}
			got := h.Restored()
			if len(got) < len(c.tried)+1 {
				t.Fatalf("what was put back: %q", got)
			}
			for i, verb := range c.tried {
				if !strings.HasPrefix(got[i], verb+" ") {
					t.Errorf("the failed try put back %q", got)
				}
			}
			// The rollback: the app's folder, from the snapshot of it as it was, and it is started again.
			if back := got[len(c.tried)]; back != "restore "+res.Before[0].Snapshot+":"+libraryVolume+" "+libraryVolume {
				t.Errorf("the rollback put back %q", got)
			}
			all := steps(h, from)
			if n := len(all); n < 4 || !slices.Contains(all[2:], "stop") || (all[n-1] != "start" && all[n-1] != "restart" && all[n-1] != "exec" && all[n-1] != "dump") {
				t.Errorf("the steps: %q", all)
			}
			if stops, starts := count(all, "stop"), count(all, "start"); stops != 2 || starts != 2 {
				t.Errorf("the app was stopped %d times and started %d: %q", stops, starts, all)
			}
		})
	}
	// A rollback that works leaves the app as it was, and says so.
	t.Run("the rollback's own dump goes back too", func(t *testing.T) {
		sock, h, library, dump := restorable(t, agent.Config{HealthWait: 50 * time.Millisecond})
		h.FailRestic("dump --target /var/lib/homeward/restore/ledger.ledger.sql -- "+dump, 1)
		_, res, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}, agent.RestoreItem{Data: "db:ledger.sql", Snapshot: dump})
		if res.State != "rolled-back" || res.Error != "dump-restore-failed" {
			t.Fatalf("the restore: %s", body)
		}
		want := []string{
			"restore " + library + ":" + libraryVolume + " " + libraryVolume, "run db", "remove /var/lib/homeward/restore/ledger.ledger.sql",
			"restore " + res.Before[0].Snapshot + ":" + libraryVolume + " " + libraryVolume, "run db",
			"dump " + res.Before[1].Snapshot + " /ledger.sql",
			"command db psql --set=ON_ERROR_STOP=1 --username=postgres ledger",
			"remove /var/lib/homeward/restore/ledger.ledger.sql",
		}
		if got := h.Restored(); !slices.Equal(got, want) {
			t.Errorf("put back:\n got %q\nwant %q", got, want)
		}
	})
}

func count(all []string, s string) int {
	n := 0
	for _, a := range all {
		if a == s {
			n++
		}
	}
	return n
}

// A dump whose app declared no restore command can't be put back: the restore is refused, never half done.
func TestADumpWithoutARestoreCommandIsRefused(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	r := withBackups()
	r.Backups[1].Restore = nil
	install(t, sock, "ledger", r)
	if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK {
		t.Fatalf("the target: %d %s", code, body)
	}
	dump := h.AddSnapshot(yesterday, "/ledger.sql", 512, "app=ledger", "data=db:ledger.sql")
	if code, _, body := restore(t, sock, agent.RestoreItem{Data: "db:ledger.sql", Snapshot: dump}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no-restore-command") {
		t.Errorf("%d %s", code, body)
	}
}

// A backup restic saved without some of its files is listed as such, and never restored: a restore makes the volume
// exactly what its snapshot holds (--delete), so it would delete what that snapshot couldn't read.
func TestAnIncompleteBackupIsNeverRestored(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _, _ := restorable(t, agent.Config{})
	h.FailRestic("data=web:/data/library --", 3)
	_, body := call(t, sock, "POST", "/v1/backups", nil)
	var run agent.Backups
	if err := json.Unmarshal(body, &run); err != nil || run.Results[0].Error != "incomplete" || run.Results[0].Snapshot == "" {
		t.Fatalf("the run: %s", body)
	}
	h.FailRestic("data=web:/data/library --", 0)
	incomplete := run.Results[0].Snapshot
	var got agent.AppBackups
	_, body = call(t, sock, "GET", "/v1/apps/ledger/backups", nil)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(got.Snapshots, func(s agent.AppSnapshot) bool { return s.ID == incomplete })
	if at < 0 || !got.Snapshots[at].Incomplete || got.Snapshots[0].Incomplete {
		t.Errorf("the listing: %+v", got.Snapshots)
	}
	from := len(h.Cmds())
	if code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: incomplete}); code != http.StatusConflict || !strings.Contains(body, "incomplete-snapshot") {
		t.Errorf("restoring it: %d %s", code, body)
	}
	if got := steps(h, from); len(got) != 0 {
		t.Errorf("the refused restore did %q", got)
	}
}

// An incomplete snapshot of the app as it is (restic exit 3) is no snapshot to roll back to: nothing is restored and
// the app is never stopped.
func TestAnIncompleteSnapshotFirstRestoresNothing(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, _ := restorable(t, agent.Config{})
	h.FailRestic("--tag app=ledger --tag data=web:/data/library --", 3)
	from := len(h.Cmds())
	if code, _, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library}); code != http.StatusBadGateway || !strings.Contains(body, "snapshot-failed") {
		t.Errorf("the restore: %d %s", code, body)
	}
	if got := steps(h, from); !slices.Equal(got, []string{"backup"}) || len(h.Restored()) != 0 {
		t.Errorf("after an incomplete snapshot first, the restore did %q and put back %q", got, h.Restored())
	}
}

// An app that doesn't stop is started again and left as it was: nothing was written, so nothing is rolled back.
func TestAnAppThatDoesntStopIsLeftAsItWas(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, _ := restorable(t, agent.Config{})
	h.Fail("systemctl stop " + appUnit)
	from := len(h.Cmds())
	code, res, body := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library})
	if code != http.StatusOK || res.State != "unchanged" || res.Error != "stop-failed" || len(res.Before) != 1 {
		t.Errorf("the restore: %d %s", code, body)
	}
	if got := steps(h, from); !slices.Equal(got, []string{"backup", "stop", "start"}) || len(h.Restored()) != 0 {
		t.Errorf("the restore did %q and put back %q", got, h.Restored())
	}
}

// While an app is restored, nothing else restarts or reinstalls it: an install is refused, and a sign-in that changes
// meanwhile reaches the app only once its restore has ended (never mid-`restic restore --delete`).
func TestNothingRestartsAnAppWhileItIsRestored(t *testing.T) {
	testenv.Isolate(t)
	sock, h, library, _ := restorable(t, agent.Config{})
	held, release := h.Hold("restore --target " + libraryVolume)
	done := make(chan agent.RestoreResult, 1)
	go func() {
		_, res, _ := restore(t, sock, agent.RestoreItem{Data: "web:/data/library", Snapshot: library})
		done <- res
	}()
	<-held
	restarts := h.Times("systemctl --no-block restart " + appUnit)
	if code, body := call(t, sock, "PUT", "/v1/apps/ledger", withBackups()); code != http.StatusConflict || !strings.Contains(string(body), "restore-running") {
		t.Errorf("an install during the restore: %d %s", code, body)
	}
	if code, body := call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test", SignIn: "login.example.test"}); code != http.StatusOK {
		t.Fatalf("the edge: %d %s", code, body)
	}
	if got := h.Times("systemctl --no-block restart " + appUnit); got != restarts {
		t.Errorf("the app was restarted mid-restore (%d times)", got-restarts)
	}
	release()
	if res := <-done; res.State != "restored" {
		t.Fatalf("the restore: %+v", res)
	}
	if got := h.Times("systemctl --no-block restart " + appUnit); got != restarts+1 {
		t.Errorf("after its restore, the app followed the sign-in with %d restarts, want 1", got-restarts)
	}
	if code, body := call(t, sock, "PUT", "/v1/apps/ledger", withBackups()); code != http.StatusAccepted {
		t.Errorf("an install after the restore: %d %s", code, body)
	}
}
