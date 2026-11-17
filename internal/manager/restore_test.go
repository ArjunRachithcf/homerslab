package manager_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

const bentoDocuments = "/var/lib/docker/volumes/homerslab-bentopdf_documents/_data"

type backupsView struct {
	App    string
	Points []struct {
		At    time.Time
		Size  uint64
		Items []struct {
			Data, What, Kind, Snapshot string
			Size                       uint64
		}
	}
	Restore *struct {
		Plan, State, Error string
		Before             []struct{ Data, Snapshot string }
	}
}

// backedUp is BentoPDF installed with data of its own and a backup target, with yesterday's backup of it in the
// node's repository (its documents and its database's dump) and this hour's.
func backedUp(t *testing.T) (p *primary, documents, dump string) {
	t.Helper()
	p = bentoWithData(t)
	made := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	p.host.Mount("homerslab-bentopdf", "web", "/data/documents", bentoDocuments)
	p.host.Mount("homerslab-bentopdf", "web", "/cache", "/var/lib/docker/volumes/homerslab-bentopdf_previews/_data")
	withTarget(t, p)
	yesterday := time.Now().Add(-24 * time.Hour)
	documents = p.host.AddSnapshot(yesterday, bentoDocuments, 4096, "app=bentopdf", "data=web:/data/documents")
	dump = p.host.AddSnapshot(yesterday, "/bento.sql", 904, "app=bentopdf", "data=web:bento.sql")
	p.host.AddSnapshot(yesterday, "/authentik.sql", 1<<20, "platform=sign-in", "data=postgresql:authentik.sql")
	p.m.BackUpDue(context.Background())
	t.Cleanup(p.m.WaitRestores)
	return p, documents, dump
}

// Admin lists each app's backups: the points it can go back to, latest first, each with its time and size, and what
// of the app each holds. Going back to one is a plan, which says what is put back from which backup and that a
// snapshot of the app as it is comes first, and changes nothing.
func TestAdminListsAnAppsBackupsAndPlansARestore(t *testing.T) {
	testenv.Isolate(t)
	p, documents, dump := backedUp(t)
	var got backupsView
	r := adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil)
	if r.json(t, &got); r.Code != http.StatusOK || got.App != "bentopdf" || len(got.Points) != 2 || got.Restore != nil {
		t.Fatalf("the app's backups: %d %s", r.Code, r.Body)
	}
	old := got.Points[1]
	if !got.Points[0].At.After(old.At) || old.Size != 5000 || len(old.Items) != 2 ||
		old.Items[0].Snapshot != documents || old.Items[0].What != "Its documents" || old.Items[0].Kind != "folder" ||
		old.Items[1].Snapshot != dump || old.Items[1].Kind != "dump" || old.Items[1].Size != 904 {
		t.Errorf("yesterday's point: %+v", old)
	}
	if strings.Contains(string(r.Body), "authentik") || strings.Contains(string(r.Body), "previews") {
		t.Errorf("the app's backups hold another's: %s", r.Body)
	}
	if r := adm(p, "GET", "/api/v1/apps/immich/backups", nil); r.Code != http.StatusNotFound {
		t.Errorf("the backups of an app that isn't installed: %d %s", r.Code, r.Body)
	}

	before, restored := len(p.host.Restic()), len(p.host.Restored())
	r = adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": old.At})
	var pl struct {
		ID, Kind string
		Risky    bool
		To       time.Time
		App      struct{ ID, Name, Installed string }
		Items    []struct{ Data, What, Snapshot string }
		Steps    []string
	}
	if r.json(t, &pl); r.Code != http.StatusCreated || pl.Kind != "restore" || !pl.Risky || !pl.To.Equal(old.At) || pl.App.Name != "BentoPDF" ||
		len(pl.Items) != 2 || pl.Items[0].Snapshot != documents || pl.Items[1].Snapshot != dump || pl.Steps[0] != "snapshot-first" {
		t.Fatalf("the restore plan: %d %s", r.Code, r.Body)
	}
	// The same plan again is the same plan, and it is read like any plan.
	if again := adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": old.At}); again.Code != http.StatusOK || !strings.Contains(string(again.Body), pl.ID) {
		t.Errorf("the plan again: %d %s", again.Code, again.Body)
	}
	if read := adm(p, "GET", "/api/v1/plans/"+pl.ID, nil); read.Code != http.StatusOK || !strings.Contains(string(read.Body), `"kind":"restore"`) {
		t.Errorf("reading the plan: %d %s", read.Code, read.Body)
	}
	if r := adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": old.At.Add(time.Minute)}); r.Code != http.StatusNotFound {
		t.Errorf("a plan for a time with no backup: %d %s", r.Code, r.Body)
	}
	if len(p.host.Restored()) != restored || len(backupsRun(p.host, before)) != 0 {
		t.Error("planning a restore changed something")
	}
}

// Applying a restore plan is a risky change: from Admin it needs the admin's passkey again (step-up), so a session
// alone, stolen or not, restores nothing. Nothing is stopped, backed up or put back. With a step-up for the plan, it
// runs.
func TestApplyingARestorePlanFromAdminNeedsAStepUp(t *testing.T) {
	testenv.Isolate(t)
	p, _, _ := backedUp(t)
	var got backupsView
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	var pl struct{ ID string }
	adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": got.Points[1].At}).json(t, &pl)
	before, cmds := len(p.host.Restic()), len(p.host.Cmds())
	r := adm(p, "POST", "/api/v1/plans/"+pl.ID+"/apply", map[string]any{})
	if r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), "step-up-required") {
		t.Errorf("applying from Admin without a step-up: %d %s", r.Code, r.Body)
	}
	p.m.WaitRestores()
	if len(p.host.Restic()) != before || len(p.host.Restored()) != 0 {
		t.Error("a restore without a step-up ran restic")
	}
	for _, c := range p.host.Cmds()[cmds:] {
		if strings.Contains(c.String(), "stop homeward-app-bentopdf") {
			t.Errorf("a restore without a step-up stopped the app: %s", c)
		}
	}
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	if got.Restore != nil {
		t.Errorf("a refused restore is kept as %+v", got.Restore)
	}
	fs := p.signIn.(*fakeSignIn)
	fs.passkeys["ines"] = 1
	a := p.adminBrowser(t)
	a.cookies["__Host-homerslab-session"] = &http.Cookie{Name: "__Host-homerslab-session", Value: p.adminSession()}
	if back := a.stepUp(fs, "plan:"+pl.ID); back != "/plans/"+pl.ID+"?step-up=done" {
		t.Fatalf("the restore's step-up: %s", back)
	}
	// A restore of the app already running (from the terminal) refuses Admin's, and leaves its step-up for later.
	held, release := p.host.Hold("restore --target " + bentoDocuments)
	if r := p.control("POST", "/api/v1/plans/"+pl.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("the terminal's restore: %d %s", r.Code, r.Body)
	}
	<-held
	if rec := a.apply(pl.ID); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "restore-running") {
		t.Errorf("Admin's restore while one runs: %d %s", rec.Code, rec.Body)
	}
	release()
	p.m.WaitRestores()
	if rec := a.apply(pl.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("applying with a step-up: %d %s", rec.Code, rec.Body)
	}
	p.m.WaitRestores()
	if len(p.host.Restored()) < 2 {
		t.Errorf("the restore with a step-up didn't run: %v", p.host.Restored())
	}
	if rec := a.apply(pl.ID); rec.Code != http.StatusForbidden {
		t.Errorf("a restore again on the spent step-up: %d %s", rec.Code, rec.Body)
	}
}

// A restore plan in, the app's state out: applied at the machine's terminal, the plan takes a snapshot of the app as it
// is, then puts back exactly the plan's backups of the app's own data, and the household keeps how it went. The
// snapshot taken first is one more point the app can go back to.
func TestARestorePlanAppliedPutsTheAppBackAsItWas(t *testing.T) {
	testenv.Isolate(t)
	p, documents, dump := backedUp(t)
	var got backupsView
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	var pl struct{ ID string }
	adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": got.Points[1].At}).json(t, &pl)
	from := len(p.host.Restic())
	if r := p.control("POST", "/api/v1/plans/"+pl.ID+"/apply", nil); r.Code != http.StatusAccepted || !strings.Contains(string(r.Body), `"running"`) {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	p.m.WaitRestores()
	if first := backupsRun(p.host, from); len(first) != 2 || !slices.Contains(first[0], "data=web:/data/documents") || !slices.Contains(first[1], "data=web:bento.sql") {
		t.Errorf("the snapshot first: %q", first)
	}
	want := []string{"restore " + documents + ":" + bentoDocuments + " " + bentoDocuments, "run web", "dump " + dump + " /bento.sql",
		"command web psql --username=postgres bento", "remove /var/lib/homeward/restore/bentopdf.bento.sql"}
	if put := p.host.Restored(); !slices.Equal(put, want) {
		t.Errorf("put back:\n got %q\nwant %q", put, want)
	}
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	if got.Restore == nil || got.Restore.Plan != pl.ID || got.Restore.State != "restored" || len(got.Restore.Before) != 2 || len(got.Points) != 3 ||
		got.Points[0].Items[0].Snapshot != got.Restore.Before[0].Snapshot {
		t.Errorf("after the restore: %+v, %d points", got.Restore, len(got.Points))
	}
	if w := restoreWarnings(p); len(w) != 0 {
		t.Errorf("a restore that worked warned: %+v", w)
	}

	// A plan made for another install of the app is stale; one for an app that is gone, too.
	stale := strings.Replace(string(p.control("GET", "/api/v1/plans/"+pl.ID, nil).Body), `"installed":"p-`, `"installed":"p-0`, 1)
	seedPlan(t, p, "r-00000000000000000000000000000000", "bentopdf", stale)
	if r := p.control("POST", "/api/v1/plans/r-00000000000000000000000000000000/apply", nil); r.Code != http.StatusConflict || !strings.Contains(string(r.Body), "stale-plan") {
		t.Errorf("a stale restore plan: %d %s", r.Code, r.Body)
	}
}

// A restore that fails is rolled back to the snapshot taken first; the household keeps that it did, and the admins
// hear of it, until a restore works.
func TestAFailedRestoreIsRolledBackAndAWarning(t *testing.T) {
	testenv.Isolate(t)
	p, documents, _ := backedUp(t)
	var got backupsView
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	var pl struct{ ID string }
	adm(p, "POST", "/api/v1/apps/bentopdf/restore-plans", map[string]any{"at": got.Points[1].At}).json(t, &pl)
	p.host.FailRestic("-- "+documents+":", 1)
	if r := p.control("POST", "/api/v1/plans/"+pl.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	p.m.WaitRestores()
	adm(p, "GET", "/api/v1/apps/bentopdf/backups", nil).json(t, &got)
	if got.Restore == nil || got.Restore.State != "rolled-back" || got.Restore.Error != "restore-failed" || len(got.Restore.Before) != 2 {
		t.Fatalf("the failed restore: %+v", got.Restore)
	}
	put := p.host.Restored()
	if len(put) != 5 || put[0] != "restore "+got.Restore.Before[0].Snapshot+":"+bentoDocuments+" "+bentoDocuments ||
		put[2] != "dump "+got.Restore.Before[1].Snapshot+" /bento.sql" {
		t.Errorf("the rollback put back %q", put)
	}
	if w := restoreWarnings(p); len(w) != 1 || w[0] != "restore-rolled-back restore-failed" {
		t.Errorf("the warnings: %q", w)
	}
	p.host.FailRestic("-- "+documents+":", 0)
	if r := p.control("POST", "/api/v1/plans/"+pl.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying again: %d %s", r.Code, r.Body)
	}
	p.m.WaitRestores()
	if w := restoreWarnings(p); len(w) != 0 {
		t.Errorf("after a restore that worked, the warnings: %q", w)
	}
}

// restoreWarnings are the open warnings about the app's restores.
func restoreWarnings(p *primary) []string {
	var w []struct{ Source, What, Resolved string }
	_ = json.Unmarshal(p.control("GET", "/api/v1/warnings", nil).Body, &w)
	var out []string
	for _, x := range w {
		if x.Source == "restore:bentopdf" && x.Resolved == "" {
			out = append(out, x.What)
		}
	}
	return out
}

// seedPlan keeps a plan as the manager's database would hold one made earlier.
func seedPlan(t *testing.T, p *primary, id, app, body string) {
	t.Helper()
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO plan (id, app, body, made_at) VALUES (?, ?, ?, 0)`, id, app, body); err != nil {
		t.Fatal(err)
	}
}

// adm asks Admin as a signed-in admin, at this household's own name for it.
func adm(p *primary, method, path string, body any) response {
	return p.admin(method, path, body, func(r *http.Request) {
		r.Host = "admin.example.test"
		if method != http.MethodGet {
			r.Header.Set("Origin", "https://admin.example.test")
		}
	})
}
