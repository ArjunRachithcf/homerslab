// Package store is the manager's database: the household's desired state and history, in SQLite. It holds no
// secrets: one-time keys and session tokens are kept only as their SHA-256 hashes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"modernc.org/sqlite" // pure Go, so the binary stays static
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("not found")

// ErrDiskFull is a write refused because the disk under the database is full. Nothing is lost: the write didn't
// happen, and writes work again once there is room.
var ErrDiskFull = errors.New("the disk that holds the database is full")

// written says which write errors are a full disk.
func written(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_FULL || errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	}
	return err
}

// Store is the open database.
type Store struct {
	db   *sql.DB
	full atomic.Bool
}

// Step is one of first-time setup's steps, as the database keeps it.
type Step struct {
	ID       string
	Half     string
	Position int
	Done     bool
	DoneAt   time.Time
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
	s := &Store{db: db}
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
func (s *Store) FillDisk() { s.full.Store(true) }

// noRoom is what a write meets once FillDisk was called: the error the system gives on a full disk.
func (s *Store) noRoom() error {
	if s.full.Load() {
		return written(&os.PathError{Op: "write", Path: "manager.db-wal", Err: syscall.ENOSPC})
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
		return written(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return written(err)
	}
	return written(tx.Commit())
}

// Steps lists setup's steps in order, with their state.
func (s *Store) Steps(ctx context.Context) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, position, half, state, done_at FROM setup_step ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var st Step
		var state string
		var doneAt sql.NullInt64
		if err := rows.Scan(&st.ID, &st.Position, &st.Half, &state, &doneAt); err != nil {
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

// CompleteStep saves a step's answers and marks it done, in one transaction.
func (s *Store) CompleteStep(ctx context.Context, id string, answers map[string]string, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for k, v := range answers {
			if _, err := tx.ExecContext(ctx, `INSERT INTO setup_answer (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
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
func (s *Store) NewLink(ctx context.Context, keyHash []byte, created, expires time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := created.UnixMilli()
		if _, err := tx.ExecContext(ctx, `UPDATE setup_link SET revoked_at = ? WHERE revoked_at IS NULL`, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE setup_session SET revoked_at = ? WHERE revoked_at IS NULL`, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO setup_link (key_hash, created_at, expires_at) VALUES (?, ?, ?)`,
			keyHash, now, expires.UnixMilli())
		return err
	})
}

// Link is a one-time link as the database keeps it. Its times are the wall clock's, for display and audit, and for
// a manager started after the link was made.
type Link struct {
	CreatedAt, ExpiresAt time.Time
}

// Link reads a link by its key's hash, or returns ErrNotFound.
func (s *Store) Link(ctx context.Context, keyHash []byte) (Link, error) {
	var created, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at, expires_at FROM setup_link WHERE key_hash = ?`, keyHash).Scan(&created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return Link{CreatedAt: time.UnixMilli(created).UTC(), ExpiresAt: time.UnixMilli(expires).UTC()}, err
}

// ClaimLink uses up a link that is neither claimed nor revoked, and opens a session in its place for the client that
// claimed it. It returns ErrNotFound when the link is unknown, claimed or revoked; whether it is too old is the
// caller's to say, by a clock the database doesn't have.
func (s *Store) ClaimLink(ctx context.Context, keyHash, sessionHash []byte, client string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE setup_link SET claimed_at = ?
			WHERE key_hash = ? AND claimed_at IS NULL AND revoked_at IS NULL`, now.UnixMilli(), keyHash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO setup_session (token_hash, client, created_at, last_seen) VALUES (?, ?, ?, ?)`,
			sessionHash, client, now.UnixMilli(), now.UnixMilli())
		return err
	})
}

// Session is a setup session as the database keeps it, with the wall clock's times.
type Session struct {
	Client              string
	CreatedAt, LastSeen time.Time
}

// Session reads a session that wasn't revoked, by its token's hash, or returns ErrNotFound.
func (s *Store) Session(ctx context.Context, tokenHash []byte) (Session, error) {
	var out Session
	var created, seen int64
	err := s.db.QueryRowContext(ctx, `SELECT client, created_at, last_seen FROM setup_session
		WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash).Scan(&out.Client, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	out.CreatedAt, out.LastSeen = time.UnixMilli(created).UTC(), time.UnixMilli(seen).UTC()
	return out, err
}

// SeeSession records when a session was last used.
func (s *Store) SeeSession(ctx context.Context, tokenHash []byte, at time.Time) error {
	if err := s.noRoom(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE setup_session SET last_seen = ? WHERE token_hash = ?`, at.UnixMilli(), tokenHash)
	return written(err)
}
