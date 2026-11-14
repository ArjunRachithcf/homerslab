package manager_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// bentoWithData is BentoPDF with data of its own: a folder, a dump, and a folder its definition leaves out.
func bentoWithData(t *testing.T) *primary {
	t.Helper()
	cat := withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		return []byte(strings.Replace(string(b), "data: []\n", `data:
  - {what: Its documents, service: web, path: /data/documents}
  - what: Its database
    service: web
    dump: {command: [pg_dump, --username=postgres, bento], file: bento.sql}
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
