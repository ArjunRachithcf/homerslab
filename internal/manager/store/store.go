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
	"time"

	_ "modernc.org/sqlite" // pure Go, so the binary stays static
)

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("not found")

// Store is the open database.
type Store struct{ db *sql.DB }

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

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
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

// ClaimLink uses up a live link and opens a session in its place, for the client that claimed it. It returns
// ErrNotFound when the link is unknown, already claimed, revoked or expired.
func (s *Store) ClaimLink(ctx context.Context, keyHash, sessionHash []byte, client string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE setup_link SET claimed_at = ?
			WHERE key_hash = ? AND claimed_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
			now.UnixMilli(), keyHash, now.UnixMilli())
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

// TouchSession keeps a session open, if it is live, asked for by the client that opened it, last seen less than idle
// ago and opened less than max ago. Otherwise it returns ErrNotFound.
func (s *Store) TouchSession(ctx context.Context, tokenHash []byte, client string, now time.Time, idle, max time.Duration) error {
	res, err := s.db.ExecContext(ctx, `UPDATE setup_session SET last_seen = ?
		WHERE token_hash = ? AND client = ? AND revoked_at IS NULL AND last_seen > ? AND created_at > ?`,
		now.UnixMilli(), tokenHash, client, now.Add(-idle).UnixMilli(), now.Add(-max).UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
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
