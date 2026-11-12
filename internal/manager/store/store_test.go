package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// SQLite's own "database or disk is full" is ErrDiskFull, whatever the write: here the database may not grow.
func TestAFullDiskIsErrDiskFull(t *testing.T) {
	testenv.Isolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "m.db"), []Step{{ID: "welcome", Half: "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var pages int
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA max_page_count = `+strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	err = s.CompleteStep(ctx, "welcome", map[string]string{"big": strings.Repeat("x", 1<<20)}, time.Now(), AnyRevision)
	if !errors.Is(err, ErrDiskFull) {
		t.Errorf("a write that needs room on a full disk: %v, want ErrDiskFull", err)
	}
	if _, err := s.Steps(ctx); err != nil {
		t.Errorf("reading on a full disk: %v", err)
	}
}

// Where ENOSPC meets fsync, ftruncate or the shared-memory file, SQLite says only "disk I/O error"; then the disk is
// full if the folder has next to no room left.
func TestAnIOErrorOnADiskWithNoRoomIsErrDiskFull(t *testing.T) {
	testenv.Isolate(t)
	const fsync, truncate, shmSize = 10 | 4<<8, 10 | 6<<8, 10 | 19<<8 // SQLITE_IOERR_FSYNC, _TRUNCATE, _SHMSIZE
	room := func(n uint64) func() (uint64, bool) { return func() (uint64, bool) { return n, true } }
	unknown := func() (uint64, bool) { return 0, false }
	for _, c := range []struct {
		code int
		free func() (uint64, bool)
		want bool
	}{
		{13, room(1 << 40), true}, // SQLITE_FULL, whatever statfs says
		{fsync, room(0), true},
		{truncate, room(4096), true},
		{shmSize, room(1 << 20), true},
		{fsync, room(1 << 30), false}, // an I/O error with room to spare is something else
		{fsync, unknown, false},
		{5, room(0), false}, // SQLITE_BUSY
	} {
		if got := full(c.code, c.free); got != c.want {
			n, _ := c.free()
			t.Errorf("code %#x with %d bytes free: full = %v, want %v", c.code, n, got, c.want)
		}
	}
}

// A database from before the boot clock (#65) opens, and its links and sessions have no boot: they have ended.
// Migrations run by index, so they only ever grow at the end: a database an integration build made, with the plans
// and without the boot clock, gets the boot clock and keeps its plans.
func TestADatabaseWithPlansButNoBootClockOpens(t *testing.T) {
	testenv.Isolate(t)
	path := filepath.Join(t.TempDir(), "m.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var made int
	for _, m := range migrations { // every migration before the boot clock
		if strings.Contains(m, "boot_id") {
			break
		}
		if _, err := old.Exec(m); err != nil {
			t.Fatal(err)
		}
		made++
	}
	if _, err := old.Exec(`PRAGMA user_version = ` + strconv.Itoa(made)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO plan (id, app, body, made_at) VALUES ('p1', 'photos', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(path, []Step{{ID: "welcome", Half: "lan"}})
	if err != nil {
		t.Fatalf("opening a database with plans and no boot clock: %v", err)
	}
	defer s.Close()
	var plans int
	if err := s.db.QueryRow(`SELECT count(*) FROM plan`).Scan(&plans); err != nil || plans != 1 {
		t.Errorf("plans kept: %d %v, want 1", plans, err)
	}
	if _, err := s.db.Exec(`SELECT boot_id, made_since_boot FROM setup_link`); err != nil {
		t.Errorf("the boot clock was not added: %v", err)
	}
}

func TestADatabaseFromBeforeTheBootClockOpens(t *testing.T) {
	testenv.Isolate(t)
	path := filepath.Join(t.TempDir(), "m.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{migrations[0], `PRAGMA user_version = 1`,
		`INSERT INTO setup_link (key_hash, created_at, expires_at) VALUES (x'01', 1, 2)`} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()
	s, err := Open(path, []Step{{ID: "welcome", Half: "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.Link(context.Background(), []byte{1})
	if err != nil || l.Made.Boot != "" {
		t.Errorf("an old link: %+v %v, want one with no boot", l, err)
	}
}
