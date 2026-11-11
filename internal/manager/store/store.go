// Package store is the manager's database: the household's desired state and history, in SQLite. It holds no
// secrets: one-time keys and session tokens are kept only as their SHA-256 hashes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite" // pure Go, so the binary stays static
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("not found")

// ErrDiskFull is a write refused because the disk under the database is full. Nothing is lost: the write didn't
// happen, and writes work again once there is room.
var ErrDiskFull = errors.New("the disk that holds the database is full")

// written says which write errors are a full disk, under the database's folder dir.
func written(err error, dir string) error {
	var se *sqlite.Error
	if err == nil {
		return nil
	}
	if errors.As(err, &se) && full(se.Code(), func() (uint64, bool) { return room(dir) }) || errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	}
	return err
}

// LittleRoom is how little room left on the database's disk makes an I/O error a full disk.
const LittleRoom = 64 << 20

// full says whether an SQLite result code is a full disk: SQLITE_FULL, or an I/O error (SQLite reports ENOSPC that
// way when it meets fsync, ftruncate or the shared-memory file) while the disk has little room left.
func full(code int, free func() (uint64, bool)) bool {
	switch code & 0xff {
	case sqlite3.SQLITE_FULL:
		return true
	case sqlite3.SQLITE_IOERR:
		n, ok := free()
		return ok && n < LittleRoom
	}
	return false
}

// room is how many bytes an unprivileged process can still write under dir.
func room(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// Store is the open database.
type Store struct {
	db   *sql.DB
	dir  string // the database's folder
	fill atomic.Bool
}

// Step is one of first-time setup's steps, as the database keeps it.
type Step struct {
	ID       string
	Half     string
	Position int
	Done     bool
	DoneAt   time.Time
	// WaitKind is what a step waits for (secret or confirm), and WaitName which; "" when it doesn't.
	WaitKind, WaitName string
}

// Drift is one thing HomersLab found changed behind its back.
type Drift struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	What   string    `json:"what"`
}

// AddDrift records drift.
func (s *Store) AddDrift(ctx context.Context, d Drift) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO drift (at, source, what) VALUES (?, ?, ?)`, d.At.UnixMilli(), d.Source, d.What)
	return err
}

// Drifts are the drift reports, newest first, at most limit.
func (s *Store) Drifts(ctx context.Context, limit int) ([]Drift, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, source, what FROM drift ORDER BY at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Drift{}
	for rows.Next() {
		var d Drift
		var at int64
		if err := rows.Scan(&at, &d.Source, &d.What); err != nil {
			return nil, err
		}
		d.At = time.UnixMilli(at).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

var migrations = []string{
	`CREATE TABLE setup_step (
		id       TEXT PRIMARY KEY,
		position INTEGER NOT NULL,
		half     TEXT NOT NULL CHECK (half IN ('lan', 'mesh')),
		state    TEXT NOT NULL DEFAULT 'todo' CHECK (state IN ('todo', 'done')),
		done_at  INTEGER
	);
	CREATE TABLE setup_answer (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE setup_link (
		key_hash   BLOB PRIMARY KEY,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		claimed_at INTEGER,
		revoked_at INTEGER
	);
	CREATE TABLE setup_session (
		token_hash BLOB PRIMARY KEY,
		client     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL,
		revoked_at INTEGER
	);`,
	// A step that waits: for a secret, which only the terminal on this machine gives (#64), by its name and never the
	// secret itself; or for the admin's yes to something the step found. And what HomersLab found changed behind its
	// back, and put back or reported.
	`CREATE TABLE setup_wait (
		step TEXT PRIMARY KEY REFERENCES setup_step (id) ON DELETE CASCADE,
		kind TEXT NOT NULL CHECK (kind IN ('secret', 'confirm')),
		name TEXT NOT NULL
	);
	CREATE TABLE drift (
		at     INTEGER NOT NULL,
		source TEXT NOT NULL,
		what   TEXT NOT NULL
	);`,
	// The DNS records HomersLab created: the source of truth for what is its own in a zone (#79). A domain's wildcard
	// certificate, which isn't secret (its key is sealed by the agent), and how its renewals go. And warnings for
	// admins, such as a renewal that failed for the first time (#80).
	`CREATE TABLE dns_record (
		zone        TEXT NOT NULL,
		name        TEXT NOT NULL,
		type        TEXT NOT NULL,
		value       TEXT NOT NULL,
		ttl_seconds INTEGER NOT NULL,
		created_at  INTEGER NOT NULL,
		PRIMARY KEY (zone, name, type, value)
	);
	CREATE TABLE certificate (
		domain        TEXT PRIMARY KEY,
		chain         TEXT NOT NULL,
		serial        TEXT NOT NULL,
		not_before    INTEGER NOT NULL,
		not_after     INTEGER NOT NULL,
		authority     TEXT NOT NULL,
		failing_since INTEGER,
		failures      INTEGER NOT NULL DEFAULT 0,
		last_error    TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE warning (
		at          INTEGER NOT NULL,
		source      TEXT NOT NULL,
		what        TEXT NOT NULL,
		resolved_at INTEGER
	);`,
	// Install plans: kept as they were made, never changed. A change in the household is a new plan, with a new id.
	`CREATE TABLE plan (
		id      TEXT PRIMARY KEY,
		app     TEXT NOT NULL,
		body    TEXT NOT NULL,
		made_at INTEGER NOT NULL
	);
	CREATE TRIGGER plan_immutable BEFORE UPDATE ON plan
	BEGIN SELECT RAISE(ABORT, 'a plan never changes: a change makes a new plan'); END;
	CREATE TRIGGER plan_kept BEFORE DELETE ON plan
	BEGIN SELECT RAISE(ABORT, 'a plan is kept: pruning old plans comes through its own path'); END;
	CREATE TRIGGER plan_once BEFORE INSERT ON plan WHEN EXISTS (SELECT 1 FROM plan WHERE id = NEW.id)
	BEGIN SELECT RAISE(ABORT, 'a plan is made once: the same id is the same plan'); END;`,
	// When a link was made and a session opened and last used, by the time since boot (nanoseconds) and that boot's
	// ID: how old they are, whatever the wall clock does (#65). Rows from before have no boot, and have ended.
	`ALTER TABLE setup_link ADD COLUMN boot_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE setup_link ADD COLUMN made_since_boot INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE setup_session ADD COLUMN boot_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE setup_session ADD COLUMN opened_since_boot INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE setup_session ADD COLUMN seen_since_boot INTEGER NOT NULL DEFAULT 0;`,
	// A claimed link's nonce, which the claiming tab sent with the key, kept as its hash, and the session the claim
	// opened: the same tab sends a claim whose answer was lost again, and gets that session back for a minute (#101).
	// Links claimed before have neither, and are never answered again.
	`ALTER TABLE setup_link ADD COLUMN nonce_hash BLOB;
	ALTER TABLE setup_link ADD COLUMN session_hash BLOB;`,
	// Admin's sessions, after a sign-in with the household account (#17): only their tokens' hashes.
	`CREATE TABLE admin_session (
		token_hash BLOB PRIMARY KEY,
		username   TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked_at INTEGER
	);`,
}

// Moment is when something happened: by the wall clock, for display and audit, and by the time since the machine
// booted, with that boot's ID, for how long ago.
type Moment struct {
	Wall  time.Time
	Boot  string
	Since time.Duration
}

// Open opens or creates the database at path, readable by its owner only, and brings its schema up to date. Steps
// are first-time setup's steps in their order; new ones are added, and every step's position and half follow the
// list.
func Open(path string, steps []Step) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer: no "database is locked" between our own connections
	s := &Store{db: db, dir: filepath.Dir(path)}
	if err := s.migrate(steps); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(steps []Step) error {
	return s.tx(context.Background(), func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			return err
		}
		for ; version < len(migrations); version++ {
			if _, err := tx.Exec(migrations[version]); err != nil {
				return fmt.Errorf("migration %d: %w", version+1, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
			return err
		}
		known := make([]any, len(steps))
		marks := make([]string, len(steps))
		for i, st := range steps {
			known[i], marks[i] = st.ID, "?"
		}
		// A step a newer version dropped leaves; its answers stay, as answers are keyed by name.
		if _, err := tx.Exec(`DELETE FROM setup_step WHERE id NOT IN (`+strings.Join(marks, ", ")+`)`, known...); err != nil {
			return err
		}
		for i, st := range steps {
			if _, err := tx.Exec(`INSERT INTO setup_step (id, position, half) VALUES (?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET position = excluded.position, half = excluded.half`, st.ID, i, st.Half); err != nil {
				return err
			}
		}
		return nil
	})
}

// FillDisk makes every later write fail as on a full disk, for tests of what a full disk looks like from outside
// the manager. SQLite's own SQLITE_FULL is ErrDiskFull just the same (store_test.go).
func (s *Store) FillDisk() { s.fill.Store(true) }

// noRoom is what a write meets once FillDisk was called: the error the system gives on a full disk.
func (s *Store) noRoom() error {
	if s.fill.Load() {
		return written(&os.PathError{Op: "write", Path: "manager.db-wal", Err: syscall.ENOSPC}, s.dir)
	}
	return nil
}

// tx runs fn in a write transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.noRoom(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return written(err, s.dir)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return written(err, s.dir)
	}
	return written(tx.Commit(), s.dir)
}

// Steps lists setup's steps in order, with their state.
func (s *Store) Steps(ctx context.Context) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, position, half, state, done_at, coalesce(w.kind, ''), coalesce(w.name, '')
		FROM setup_step LEFT JOIN setup_wait w ON w.step = setup_step.id ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var st Step
		var state string
		var doneAt sql.NullInt64
		if err := rows.Scan(&st.ID, &st.Position, &st.Half, &state, &doneAt, &st.WaitKind, &st.WaitName); err != nil {
			return nil, err
		}
		st.Done = state == "done"
		if doneAt.Valid {
			st.DoneAt = time.UnixMilli(doneAt.Int64).UTC()
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func saveAnswers(ctx context.Context, tx *sql.Tx, answers map[string]string) error {
	for k, v := range answers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO setup_answer (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return nil
}

// SaveAnswers keeps answers, none of them secret.
func (s *Store) SaveAnswers(ctx context.Context, answers map[string]string) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return saveAnswers(ctx, tx, answers) })
}

// Wait keeps a step's answers so far and marks it waiting, for a secret or a confirmation, by name.
func (s *Store) Wait(ctx context.Context, id, kind, name string, answers map[string]string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := saveAnswers(ctx, tx, answers); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE setup_step SET state = 'todo', done_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO setup_wait (step, kind, name) VALUES (?, ?, ?)
			ON CONFLICT (step) DO UPDATE SET kind = excluded.kind, name = excluded.name`, id, kind, name)
		return err
	})
}

// CompleteStep saves a step's answers and marks it done, and no longer waiting, in one transaction.
func (s *Store) CompleteStep(ctx context.Context, id string, answers map[string]string, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := saveAnswers(ctx, tx, answers); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM setup_wait WHERE step = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE setup_step SET state = 'done', done_at = ? WHERE id = ?`, at.UnixMilli(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return nil
	})
}

// Answers are what the admin has answered so far, none of it secret.
func (s *Store) Answers(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM setup_answer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// NewLink revokes every earlier link and session, and keeps a new link's key hash.
func (s *Store) NewLink(ctx context.Context, keyHash []byte, made Moment, expires time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := made.Wall.UnixMilli()
		if _, err := tx.ExecContext(ctx, `UPDATE setup_link SET revoked_at = ? WHERE revoked_at IS NULL`, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE setup_session SET revoked_at = ? WHERE revoked_at IS NULL`, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO setup_link (key_hash, created_at, expires_at, boot_id, made_since_boot)
			VALUES (?, ?, ?, ?, ?)`, keyHash, now, expires.UnixMilli(), made.Boot, int64(made.Since))
		return err
	})
}

// Link is a one-time link as the database keeps it.
type Link struct {
	Made      Moment
	ExpiresAt time.Time
}

// Link reads a link by its key's hash, or returns ErrNotFound.
func (s *Store) Link(ctx context.Context, keyHash []byte) (Link, error) {
	var created, expires, since int64
	var l Link
	err := s.db.QueryRowContext(ctx, `SELECT created_at, expires_at, boot_id, made_since_boot FROM setup_link
		WHERE key_hash = ?`, keyHash).Scan(&created, &expires, &l.Made.Boot, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	l.Made.Wall, l.Made.Since, l.ExpiresAt = time.UnixMilli(created).UTC(), time.Duration(since), time.UnixMilli(expires).UTC()
	return l, err
}

// ClaimLink uses up a link that is neither claimed nor revoked, keeps the hash of the nonce it was claimed with, and
// opens a session in its place for the client that claimed it. It returns ErrNotFound when the link is unknown,
// claimed or revoked; whether it is too old is the caller's to say.
func (s *Store) ClaimLink(ctx context.Context, keyHash, nonceHash, sessionHash []byte, client string, now Moment) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE setup_link SET claimed_at = ?, nonce_hash = ?, session_hash = ?
			WHERE key_hash = ? AND claimed_at IS NULL AND revoked_at IS NULL`, now.Wall.UnixMilli(), nonceHash, sessionHash, keyHash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO setup_session (token_hash, client, created_at, last_seen, boot_id,
			opened_since_boot, seen_since_boot) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sessionHash, client, now.Wall.UnixMilli(), now.Wall.UnixMilli(), now.Boot, int64(now.Since), int64(now.Since))
		return err
	})
}

// Claim is a claimed link as the database keeps it: the hash of the nonce it was claimed with, and the session the
// claim opened.
type Claim struct {
	NonceHash, SessionHash []byte
	Session                Session
}

// Claim reads the claim of a link that was claimed and not revoked, whose session wasn't revoked, by the link's key
// hash, or returns ErrNotFound. Whether the session is too old or idle is the caller's to say.
func (s *Store) Claim(ctx context.Context, keyHash []byte) (Claim, error) {
	var c Claim
	var created, seen, openedSince, seenSince int64
	err := s.db.QueryRowContext(ctx, `SELECT l.nonce_hash, l.session_hash, s.client, s.created_at, s.last_seen, s.boot_id,
			s.opened_since_boot, s.seen_since_boot
		FROM setup_link l JOIN setup_session s ON s.token_hash = l.session_hash
		WHERE l.key_hash = ? AND l.claimed_at IS NOT NULL AND l.revoked_at IS NULL AND l.nonce_hash IS NOT NULL
			AND s.revoked_at IS NULL`, keyHash).
		Scan(&c.NonceHash, &c.SessionHash, &c.Session.Client, &created, &seen, &c.Session.Opened.Boot, &openedSince, &seenSince)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrNotFound
	}
	c.Session.Seen.Boot = c.Session.Opened.Boot
	c.Session.Opened.Wall, c.Session.Opened.Since = time.UnixMilli(created).UTC(), time.Duration(openedSince)
	c.Session.Seen.Wall, c.Session.Seen.Since = time.UnixMilli(seen).UTC(), time.Duration(seenSince)
	return c, err
}

// RenewClaim gives a claim's session a new token in place of the one it had: the same session, opened and last used
// when it was, which the old token no longer opens. It returns ErrNotFound when the link was revoked, its session
// revoked or opened in another boot than now's, or its token changed meanwhile.
func (s *Store) RenewClaim(ctx context.Context, keyHash, oldSession, newSession []byte, now Moment) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE setup_link SET session_hash = ?
			WHERE key_hash = ? AND session_hash = ? AND revoked_at IS NULL`, newSession, keyHash, oldSession)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		res, err = tx.ExecContext(ctx, `UPDATE setup_session SET token_hash = ?
			WHERE token_hash = ? AND revoked_at IS NULL AND boot_id = ?`, newSession, oldSession, now.Boot)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return nil
	})
}

// Session is a setup session as the database keeps it.
type Session struct {
	Client       string
	Opened, Seen Moment
}

// Session reads a session that wasn't revoked, by its token's hash, or returns ErrNotFound.
func (s *Store) Session(ctx context.Context, tokenHash []byte) (Session, error) {
	var out Session
	var created, seen, openedSince, seenSince int64
	err := s.db.QueryRowContext(ctx, `SELECT client, created_at, last_seen, boot_id, opened_since_boot, seen_since_boot
		FROM setup_session WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash).
		Scan(&out.Client, &created, &seen, &out.Opened.Boot, &openedSince, &seenSince)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	out.Seen.Boot = out.Opened.Boot
	out.Opened.Wall, out.Opened.Since = time.UnixMilli(created).UTC(), time.Duration(openedSince)
	out.Seen.Wall, out.Seen.Since = time.UnixMilli(seen).UTC(), time.Duration(seenSince)
	return out, err
}

// SeeSession records when a session was last used, in the boot it was opened in.
func (s *Store) SeeSession(ctx context.Context, tokenHash []byte, at Moment) error {
	if err := s.noRoom(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE setup_session SET last_seen = ?, seen_since_boot = ? WHERE token_hash = ? AND boot_id = ?`,
		at.Wall.UnixMilli(), int64(at.Since), tokenHash, at.Boot)
	return written(err, s.dir)
}

// SavePlan keeps a plan by its id, unless one with that id is kept already, and returns the one kept and whether it
// is new: the same id is the same plan, made earlier.
func (s *Store) SavePlan(ctx context.Context, id, app string, body []byte, madeAt time.Time) (kept []byte, made bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT body FROM plan WHERE id = ?`, id).Scan(&kept)
		if !errors.Is(err, sql.ErrNoRows) {
			return err // kept earlier, or a failure
		}
		made, kept = true, body
		_, err = tx.ExecContext(ctx, `INSERT INTO plan (id, app, body, made_at) VALUES (?, ?, ?, ?)`, id, app, string(body), madeAt.UnixMilli())
		return err
	})
	return kept, made, err
}

// Plan is the plan kept with that id, or ErrNotFound.
func (s *Store) Plan(ctx context.Context, id string) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM plan WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return body, err
}

// NewAdminSession keeps a new Admin session's token hash, for a person, until expires.
func (s *Store) NewAdminSession(ctx context.Context, tokenHash []byte, username string, created, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_session (token_hash, username, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, username, created.UnixMilli(), expires.UnixMilli())
	return err
}

// AdminSession is the person a live Admin session is for, or ErrNotFound.
func (s *Store) AdminSession(ctx context.Context, tokenHash []byte, now time.Time) (string, error) {
	var u string
	err := s.db.QueryRowContext(ctx, `SELECT username FROM admin_session WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ? AND created_at <= ?`,
		tokenHash, now.UnixMilli(), now.UnixMilli()).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return u, err
}

// EndAdminSession revokes an Admin session.
func (s *Store) EndAdminSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_session SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, now.UnixMilli(), tokenHash)
	return err
}
