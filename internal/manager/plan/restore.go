package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Restoring an app to an earlier point (#22; spec: Updates, removal, eject): a plan, shown before anything happens,
// that takes a snapshot of the app as it is first. It is always a risky change.

// Backup is one backup of one thing of an app's, as the node's repository lists it.
type Backup struct {
	ID   string
	Time time.Time
	Data string // the data's name among its app's: its service and its folder, or its dump's file
	Size uint64
	// Incomplete: restic saved it without some of its files.
	Incomplete bool
}

// Kept is one thing of an app's that its install plan backs up.
type Kept struct {
	Data string `json:"data"`
	What string `json:"what"`
	Kind string `json:"kind"` // folder or dump
}

// Point is a point an app can be restored to: one run's backups of its data, each thing's own.
type Point struct {
	At    time.Time   `json:"at"` // when the run's last backup of the app was made
	Size  uint64      `json:"size"`
	Items []PointItem `json:"items"`
}

// PointItem is one thing of the app's at a point, and the backup that holds it.
type PointItem struct {
	Kept
	Snapshot string    `json:"snapshot"`
	At       time.Time `json:"at"`
	Size     uint64    `json:"size"`
}

// Points are the points an app can be restored to, latest first, from its backups: a run's backups of one app all
// carry the run's time (backup.Item.Time), so a point is the backups made at one time, to the second. Backups of data
// the app's install plan doesn't keep, and incomplete ones, are left out.
func Points(kept []Kept, backups []Backup) []Point {
	backups = slices.Clone(backups)
	slices.SortStableFunc(backups, func(a, b Backup) int { return a.Time.Compare(b.Time) })
	points := []Point{}
	for _, b := range backups {
		at := slices.IndexFunc(kept, func(k Kept) bool { return k.Data == b.Data })
		if at < 0 || b.Incomplete {
			continue
		}
		when := b.Time.UTC().Truncate(time.Second)
		if n := len(points); n == 0 || !points[n-1].At.Equal(when) ||
			slices.ContainsFunc(points[n-1].Items, func(i PointItem) bool { return i.Data == b.Data }) {
			points = append(points, Point{At: when, Items: []PointItem{}})
		}
		cur := &points[len(points)-1]
		cur.Items = append(cur.Items, PointItem{Kept: kept[at], Snapshot: b.ID, At: b.Time.UTC(), Size: b.Size})
		cur.Size += b.Size
	}
	// Each point's things in the order the install plan lists them.
	for _, p := range points {
		slices.SortStableFunc(p.Items, func(a, b PointItem) int {
			return slices.IndexFunc(kept, func(k Kept) bool { return k.Data == a.Data }) - slices.IndexFunc(kept, func(k Kept) bool { return k.Data == b.Data })
		})
	}
	slices.Reverse(points)
	return points
}

// Restore is one restore plan. Like every plan it is immutable, and its id is a hash of what it says.
type Restore struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"` // restore
	MadeAt time.Time `json:"madeAt"`
	App    Restored  `json:"app"`
	// To is the point the app goes back to; Items what is put back, each from its backup.
	To    time.Time   `json:"to"`
	Items []PointItem `json:"items"`
	// Stays is what the app's backups keep that this point doesn't hold: it stays as it is now.
	Stays []Kept `json:"stays"`
	// Steps are what applying does, in order.
	Steps []string `json:"steps"`
	// Risky: applying always needs the admin's passkey.
	Risky bool `json:"risky"`
}

// Restored is the app a restore plan restores, and the install plan it runs by.
type Restored struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed string `json:"installed"`
}

// RestoreSteps are what applying a restore plan does, in order: nothing changes unless the snapshot of the app as it
// is was taken; the dumps go back with only their own services running; a failure puts that snapshot back.
var RestoreSteps = []string{"snapshot-first", "stop", "restore-folders", "restore-dumps", "start", "health-check", "roll-back-if-it-fails"}

// ErrNoSuchPoint is a point the app has no backup at.
var ErrNoSuchPoint = errors.New("no-such-backup")

// MakeRestore plans restoring the app to the point at.
func MakeRestore(app Restored, kept []Kept, backups []Backup, at, made time.Time) (Restore, error) {
	for _, p := range Points(kept, backups) {
		if !p.At.Equal(at) {
			continue
		}
		r := Restore{Kind: "restore", App: app, To: p.At, Items: p.Items, Stays: []Kept{}, Steps: RestoreSteps, Risky: true}
		for _, k := range kept {
			if !slices.ContainsFunc(p.Items, func(i PointItem) bool { return i.Data == k.Data }) {
				r.Stays = append(r.Stays, k)
			}
		}
		b, err := json.Marshal(r)
		if err != nil {
			return Restore{}, err
		}
		sum := sha256.Sum256(b)
		r.ID, r.MadeAt = "r-"+hex.EncodeToString(sum[:16]), made.UTC()
		return r, nil
	}
	return Restore{}, ErrNoSuchPoint
}
