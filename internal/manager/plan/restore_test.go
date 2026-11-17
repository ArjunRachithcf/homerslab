package plan_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/manager/plan"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// An app's backups are listed as points it can be restored to, latest first: each run's backups of its things, with
// the point's time and size. A restore plan for a point names each thing's own backup, what stays as it is, and the
// steps, the snapshot first; it is always a risky change, and the same plan has the same id.
func TestARestorePlanSaysWhatGoesBackFromWhichBackup(t *testing.T) {
	testenv.Isolate(t)
	nine := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	kept := []plan.Kept{{Data: "server:/data", What: "The photo library", Kind: "folder"}, {Data: "database:immich.sql", What: "Its database", Kind: "dump"}}
	backups := []plan.Backup{
		{ID: "b2", Time: nine.Add(time.Hour), Data: "database:immich.sql", Size: 30},
		{ID: "a1", Time: nine, Data: "server:/data", Size: 1000},
		{ID: "a2", Time: nine, Data: "database:immich.sql", Size: 20},
		{ID: "b1", Time: nine.Add(time.Hour), Data: "server:/data", Size: 1100},
		{ID: "c1", Time: nine.Add(2 * time.Hour), Data: "server:/data", Size: 1200},                // the dump failed in this run
		{ID: "x", Time: nine.Add(2 * time.Hour), Data: "server:/cache", Size: 1},                   // no longer kept
		{ID: "d1", Time: nine.Add(3 * time.Hour), Data: "server:/data", Size: 1, Incomplete: true}, // restic couldn't read it all
		{ID: "e2", Time: nine.Add(4 * time.Hour), Data: "database:immich.sql", Size: 40},           // the library failed in this run
	}
	points := plan.Points(kept, backups)
	if len(points) != 4 || !points[0].At.Equal(nine.Add(4*time.Hour)) || len(points[0].Items) != 1 || points[0].Items[0].Snapshot != "e2" ||
		!points[1].At.Equal(nine.Add(2*time.Hour)) || len(points[1].Items) != 1 || points[1].Items[0].Snapshot != "c1" ||
		!points[2].At.Equal(nine.Add(time.Hour)) || points[2].Size != 1130 ||
		points[3].Items[0].Snapshot != "a1" || points[3].Items[1].Snapshot != "a2" || points[3].Items[1].What != "Its database" {
		t.Fatalf("the points: %+v", points)
	}
	// Two runs that each failed one thing are two points, never one of things that never were together.
	app := plan.Restored{ID: "immich", Name: "Immich", Installed: "p-1"}
	r, err := plan.MakeRestore(app, kept, backups, points[2].At, nine.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "restore" || !r.Risky || len(r.Items) != 2 || r.Items[0].Snapshot != "b1" || r.Items[1].Snapshot != "b2" || len(r.Stays) != 0 ||
		r.Steps[0] != "snapshot-first" || !slices.Contains(r.Steps, "roll-back-if-it-fails") {
		t.Errorf("the plan: %+v", r)
	}
	again, _ := plan.MakeRestore(app, kept, backups, points[2].At, nine.Add(6*time.Hour))
	other, _ := plan.MakeRestore(app, kept, backups, points[3].At, nine.Add(5*time.Hour))
	if again.ID != r.ID || other.ID == r.ID || len(r.ID) != 34 {
		t.Errorf("the plans' ids: %s, again %s, another point's %s", r.ID, again.ID, other.ID)
	}
	late, err := plan.MakeRestore(app, kept, backups, points[1].At, nine)
	if err != nil || len(late.Stays) != 1 || late.Stays[0].What != "Its database" {
		t.Errorf("a point without the dump: %+v %v", late, err)
	}
	for _, at := range []time.Time{nine.Add(time.Minute), nine.Add(3 * time.Hour)} {
		if _, err := plan.MakeRestore(app, kept, backups, at, nine); !errors.Is(err, plan.ErrNoSuchPoint) {
			t.Errorf("a time with no backup, or an incomplete one: %v", err)
		}
	}
}
