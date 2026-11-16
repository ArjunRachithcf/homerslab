package manager_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// bentoWithData is BentoPDF with data of its own: a folder, a dump, and a folder its definition leaves out.
func bentoWithData(t *testing.T) *primary {
	t.Helper()
	cat := withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		return []byte(strings.Replace(string(b), "data: []\n", `data:
  - {what: Its documents, service: web, path: /data/documents}
  - what: Its database
    service: web
    dump: {command: [pg_dump, --username=postgres, bento], file: bento.sql, restore: [psql, --username=postgres, bento]}
  - {what: Its previews, service: web, path: /cache, backup: false, why: They are made again}
`, 1))
	}, "apps/bentopdf/compose.yaml": func(b []byte) []byte {
		return []byte(strings.Replace(string(b), "    security_opt: [no-new-privileges:true]\n",
			"    security_opt: [no-new-privileges:true]\n    volumes: [documents:/data/documents, previews:/cache]\n", 1) + "volumes: {documents: {}, previews: {}}\n")
	}})
	p, _ := installable(t, cat)
	return p
}

// withTarget sets a disk as the household's backup target, as `homerslab backup target` would on the console.
func withTarget(t *testing.T, p *primary) {
	t.Helper()
	r := p.control("PUT", "/api/v1/backups/target", map[string]any{"target": map[string]string{"kind": "disk", "path": "/srv/backups"}})
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"/srv/backups/primary"`) {
		t.Fatalf("the target: %d %s", r.Code, r.Body)
	}
}

func backupsRun(h *agenttest.Host, from int) [][]string {
	var out [][]string
	for _, r := range h.Restic()[from:] {
		if i := slices.Index(r.Argv, "backup"); i >= 0 {
			out = append(out, r.Argv[i:])
		}
	}
	return out
}

func fmtRuns(runs [][]string) string {
	var b strings.Builder
	for _, a := range runs {
		b.WriteString(strings.Join(a, " ") + "\n")
	}
	return b.String()
}

// Every hour, the scheduler backs up every app's data exactly as its plan said (its folders and dumps, never what the
// plan leaves out), the sign-in's database and the manager's own; the node's key, made once, is the primary's to keep.
func TestEveryHourTheHouseholdIsBackedUpAsThePlansSaid(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	made := planFor(t, p, "bentopdf")
	var pl struct {
		Backups struct {
			Data []struct{ What, Kind string }
			Left []struct{ What, Why string }
		}
	}
	p.control("GET", "/api/v1/plans/"+made.ID, nil).json(t, &pl)
	if len(pl.Backups.Data) != 2 || pl.Backups.Data[1].Kind != "dump" || len(pl.Backups.Left) != 1 {
		t.Fatalf("the plan's backups: %+v", pl.Backups)
	}
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	p.host.Mount("homerslab-bentopdf", "web", "/data/documents", "/var/lib/docker/volumes/homerslab-bentopdf_documents/_data")
	p.host.Mount("homerslab-bentopdf", "web", "/cache", "/var/lib/docker/volumes/homerslab-bentopdf_previews/_data")

	ctx := context.Background()
	p.m.BackUpDue(ctx)
	if n := len(p.host.Restic()); n != 0 {
		t.Fatalf("restic ran before there was a target: %d runs", n)
	}
	withTarget(t, p)
	var key struct{ Value string }
	serveAgent(t, p.agent, "GET", "/v1/credentials/manager-backup-key-primary").json(t, &key)
	if len(key.Value) != 64 {
		t.Fatalf("the primary holds no key for the node's repository")
	}

	from := len(p.host.Restic())
	p.m.BackUpDue(ctx)
	runs := backupsRun(p.host, from)
	var items []string
	for _, a := range runs {
		for i, s := range a {
			if s == "--tag" && strings.HasPrefix(a[i+1], "data=") {
				items = append(items, a[i+1])
			}
		}
	}
	if !slices.Equal(items, []string{"data=manager:manager.db", "data=web:/data/documents", "data=web:bento.sql", "data=postgresql:authentik.sql"}) {
		t.Errorf("an hour's run backed up %q", items)
	}
	if !slices.Contains(runs[1], "/var/lib/docker/volumes/homerslab-bentopdf_documents/_data") || strings.Contains(fmtRuns(runs), "previews") {
		t.Errorf("the app's folders: %q", runs)
	}
	for _, r := range p.host.Restic() {
		if strings.Contains(strings.Join(r.Argv, " ")+strings.Join(r.Unit, " "), key.Value) {
			t.Error("the repository's key is on a command line")
		}
	}
	if !strings.HasPrefix(string(p.host.Restic()[from+1].Stdin), "SQLite format 3\x00") {
		t.Error("the manager's database isn't what restic read")
	}

	// Within the hour, nothing runs again; an hour on, it does. Prune runs once a day.
	from = len(p.host.Restic())
	p.clock.Add(30 * time.Minute)
	p.m.BackUpDue(ctx)
	if n := len(p.host.Restic()) - from; n != 0 {
		t.Errorf("half an hour on, %d runs", n)
	}
	p.clock.Add(30 * time.Minute)
	p.m.BackUpDue(ctx)
	if runs := backupsRun(p.host, from); len(runs) != 4 {
		t.Errorf("an hour on: %q", runs)
	}
	prunes := 0
	for _, r := range p.host.Restic() {
		if slices.Contains(r.Argv, "forget") {
			prunes++
		}
	}
	if prunes != 1 {
		t.Errorf("%d prunes in an hour", prunes)
	}
}

// A backup that fails is a warning, once, until a run backs the same up again.
func TestAFailedBackupIsAWarningUntilOneWorks(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	made := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	p.host.Mount("homerslab-bentopdf", "web", "/data/documents", "/var/lib/docker/volumes/homerslab-bentopdf_documents/_data")
	withTarget(t, p)
	p.host.FailRestic("data=web:bento.sql", 1)
	warnings := func() []struct{ Source, What, Resolved string } {
		var w []struct{ Source, What, Resolved string }
		_ = json.Unmarshal(p.control("GET", "/api/v1/warnings", nil).Body, &w)
		return w
	}
	for range 2 {
		p.m.BackUpDue(context.Background())
		p.clock.Add(time.Hour)
	}
	w := warnings()
	if len(w) != 1 || w[0].Source != "backup:bentopdf" || w[0].What != "backup-failed web:bento.sql failed" || w[0].Resolved != "" {
		t.Fatalf("the warnings: %+v", w)
	}
	p.host.FailRestic("data=web:bento.sql", 0)
	p.m.BackUpDue(context.Background())
	if w := warnings(); len(w) != 1 || w[0].Resolved == "" {
		t.Errorf("after a run that worked: %+v", w)
	}
}

// The target is checked before anything is sealed or sent: only plain settings reach the agent.
func TestABadTargetIsRefused(t *testing.T) {
	testenv.Isolate(t)
	p, _ := installable(t, nil)
	for _, tg := range []map[string]any{{"kind": "disk", "path": "relative"}, {"kind": "sftp", "host": "nas", "user": "u", "path": "/x"}, {"kind": "b2"}} {
		if r := p.control("PUT", "/api/v1/backups/target", map[string]any{"target": tg}); r.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", tg, r.Code, r.Body)
		}
	}
	if len(p.host.Restic()) != 0 {
		t.Error("restic ran for a bad target")
	}
}

// A run that hangs is ended, and once no run has got to its end for two hours, the admins hear of it; a run that does
// resolves it.
func TestBackupsThatStopEndingAreAWarning(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	withTarget(t, p)
	late := func() (open, resolved bool) {
		var w []struct{ Source, Resolved string }
		_ = json.Unmarshal(p.control("GET", "/api/v1/warnings", nil).Body, &w)
		for _, x := range w {
			if x.Source == "backup:late" {
				open, resolved = x.Resolved == "", x.Resolved != ""
			}
		}
		return open, resolved
	}
	p.host.Hang("systemd-run")
	for range 3 {
		start := time.Now()
		p.m.BackUpDue(context.Background())
		if time.Since(start) > 30*time.Second {
			t.Fatal("a hung run wasn't ended")
		}
		p.clock.Add(time.Hour)
	}
	if open, _ := late(); !open {
		t.Fatal("three hours of hung runs, and no warning")
	}
	p.host.Unhang()
	p.m.BackUpDue(context.Background())
	if open, resolved := late(); open || !resolved {
		t.Errorf("after a run that got to its end: open %v, resolved %v", open, resolved)
	}
}

const b2Secret = "K005-the-b2-application-key/0123456789"

// withB2 sets a B2 bucket as the household's target, at the terminal, with the quota given (0 for none).
func withB2(t *testing.T, p *primary, quota uint64) response {
	t.Helper()
	r := p.control("PUT", "/api/v1/backups/target", map[string]any{
		"target":      map[string]any{"kind": "b2", "region": "eu-central-003", "bucket": "household", "path": "homerslab", "quotaBytes": quota},
		"credentials": map[string]string{"accessKeyId": "005b2keyid0000000001", "secretAccessKey": b2Secret}})
	if r.Code != http.StatusOK {
		t.Fatalf("the target: %d %s", r.Code, r.Body)
	}
	return r
}

// Admin shows where backups go and whether a box taken over could delete them, never the target's credentials.
// Changing the target from Admin is a risky change, which needs a step-up (stepup_test.go): without one, Admin refuses.
func TestAdminShowsTheBackupTargetAndNeverItsCredentials(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	if r := adm(p, "GET", "/api/v1/backups/target", nil); r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"target":null`) {
		t.Fatalf("before a target: %d %s", r.Code, r.Body)
	}
	until := p.clock.Now().AddDate(0, 0, 30).UTC()
	p.host.BucketLocked(backup.Immutability{Immutable: true, Why: "compliance", Days: 30, LockedUntil: until})
	if r := withB2(t, p, 0); !strings.Contains(string(r.Body), `"immutability":{"immutable":true,"why":"compliance","days":30,"at":`) {
		t.Errorf("setting it: %s", r.Body)
	}
	var st struct {
		Target struct {
			Kind, Region, Bucket string
		}
		Repository   string
		Immutability backup.Immutability
	}
	r := adm(p, "GET", "/api/v1/backups/target", nil)
	r.json(t, &st)
	if st.Target.Kind != "b2" || st.Target.Region != "eu-central-003" || st.Repository != "s3:https://s3.eu-central-003.backblazeb2.com/household/homerslab/primary" ||
		!st.Immutability.Immutable || st.Immutability.Why != "compliance" || st.Immutability.At.IsZero() || !st.Immutability.LockedUntil.Equal(until) {
		t.Errorf("Admin's view: %s", r.Body)
	}
	// The day's prune reads the bucket's lock again: Admin shows what it found, and when.
	p.host.BucketLocked(backup.Immutability{Why: "no-default-retention"})
	p.m.BackUpDue(context.Background())
	adm(p, "GET", "/api/v1/backups/target", nil).json(t, &st)
	if st.Immutability.Immutable || st.Immutability.Why != "no-default-retention" || st.Immutability.At.IsZero() {
		t.Errorf("after the bucket's lock was turned off and a prune: %+v", st.Immutability)
	}
	for _, r := range []response{r, p.control("GET", "/api/v1/backups/target", nil)} {
		if strings.Contains(string(r.Body), b2Secret) || strings.Contains(string(r.Body), "accessKeyId") {
			t.Errorf("the target's credentials are shown: %s", r.Body)
		}
	}
	before := len(p.host.Restic())
	change := adm(p, "PUT", "/api/v1/backups/target", map[string]any{"target": map[string]string{"kind": "disk", "path": "/srv/backups"}})
	if change.Code != http.StatusForbidden || !strings.Contains(string(change.Body), "step-up-required") {
		t.Errorf("changing it from Admin without a step-up: %d %s", change.Code, change.Body)
	}
	if n := len(p.host.Restic()) - before; n != 0 || !strings.Contains(string(adm(p, "GET", "/api/v1/backups/target", nil).Body), `"kind":"b2"`) {
		t.Errorf("a change refused ran restic %d times, or changed the target", n)
	}
}

// What the lock run locked is locked until it runs out: past that, with no run since, the target isn't immutable.
func TestALockThatHasRunOutIsNotImmutable(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	p.host.BucketLocked(backup.Immutability{Immutable: true, Why: "compliance", Days: 30, LockedUntil: p.clock.Now().AddDate(0, 0, 30).UTC()})
	withB2(t, p, 0)
	var st struct{ Immutability backup.Immutability }
	p.clock.Add(29 * 24 * time.Hour)
	if p.control("GET", "/api/v1/backups/target", nil).json(t, &st); !st.Immutability.Immutable {
		t.Errorf("before the lock runs out: %+v", st.Immutability)
	}
	p.clock.Add(2 * 24 * time.Hour)
	if p.control("GET", "/api/v1/backups/target", nil).json(t, &st); st.Immutability.Immutable || st.Immutability.Why != "lock-expires" {
		t.Errorf("past the lock: %+v", st.Immutability)
	}
}

// A target that is 80% full is a warning, once, before prune can no longer free space, until it is less full. A target
// that doesn't say its size never warns.
func TestATargetEightyPercentFullIsAWarningUntilItIsnt(t *testing.T) {
	testenv.Isolate(t)
	p := bentoWithData(t)
	warnings := func() []struct{ Source, What, Resolved string } {
		var w, out []struct{ Source, What, Resolved string }
		_ = json.Unmarshal(p.control("GET", "/api/v1/warnings", nil).Body, &w)
		for _, x := range w {
			if x.Source == "backup:space" {
				out = append(out, x)
			}
		}
		return out
	}
	withB2(t, p, 1000)
	run := func(used uint64) {
		p.host.RepoSize(used)
		p.m.BackUpDue(context.Background())
		p.clock.Add(time.Hour)
	}
	run(799)
	if w := warnings(); len(w) != 0 {
		t.Errorf("79.9%% full: %+v", w)
	}
	var st struct {
		Space struct{ UsedBytes, SizeBytes uint64 }
	}
	adm(p, "GET", "/api/v1/backups/target", nil).json(t, &st)
	if st.Space.UsedBytes != 799 || st.Space.SizeBytes != 1000 {
		t.Errorf("Admin's view of the space: %+v", st.Space)
	}
	run(800)
	if w := warnings(); len(w) != 1 || w[0].Resolved != "" {
		t.Errorf("exactly 80%% full: %+v", w)
	}
	run(950)
	if w := warnings(); len(w) != 1 || w[0].What != "target-full 80" || w[0].Resolved != "" {
		t.Fatalf("80%% and 95%% full: %+v", w)
	}
	run(500)
	if w := warnings(); len(w) != 1 || w[0].Resolved == "" {
		t.Errorf("half full again: %+v", w)
	}
	// Full again, then another target: the old one's warning isn't the new one's.
	run(900)
	withB2(t, p, 1<<40)
	if w := warnings(); len(w) != 2 || w[0].Resolved == "" || w[1].Resolved == "" {
		t.Errorf("after a new target: %+v", w)
	}

	q := bentoWithData(t)
	withB2(t, q, 0)
	q.host.RepoSize(1 << 50)
	q.m.BackUpDue(context.Background())
	var all []struct{ Source string }
	_ = json.Unmarshal(q.control("GET", "/api/v1/warnings", nil).Body, &all)
	for _, w := range all {
		if w.Source == "backup:space" {
			t.Errorf("a target of unknown size warned: %+v", all)
		}
	}
}
