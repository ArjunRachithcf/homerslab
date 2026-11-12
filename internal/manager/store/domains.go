package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
)

// Book is the list of DNS records HomersLab created, as the DNS adapter keeps it.
func (s *Store) Book() dns.Book { return book{s} }

type book struct{ s *Store }

func (b book) Records(ctx context.Context, zone string) ([]dns.Record, error) {
	rows, err := b.s.db.QueryContext(ctx, `SELECT name, type, value, ttl_seconds FROM dns_record
		WHERE zone = ? ORDER BY created_at, rowid`, zone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dns.Record
	for rows.Next() {
		var r dns.Record
		var ttl int64
		if err := rows.Scan(&r.Name, &r.Type, &r.Value, &ttl); err != nil {
			return nil, err
		}
		r.TTL = time.Duration(ttl) * time.Second
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b book) Keep(ctx context.Context, zone string, r dns.Record) error {
	_, err := b.s.db.ExecContext(ctx, `INSERT INTO dns_record (zone, name, type, value, ttl_seconds, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, zone, r.Name, r.Type, r.Value, int64(r.TTL/time.Second), time.Now().UnixMilli())
	return err
}

func (b book) Forget(ctx context.Context, zone string, r dns.Record) error {
	_, err := b.s.db.ExecContext(ctx, `DELETE FROM dns_record WHERE zone = ? AND name = ? AND type = ? AND value = ?`, zone, r.Name, r.Type, r.Value)
	return err
}

// Certificate is a domain's wildcard certificate as the database keeps it: its chain and dates, never its key, and
// how its renewals go.
type Certificate struct {
	Domain       string    `json:"domain"`
	Chain        []byte    `json:"-"`
	Serial       string    `json:"serial"`
	NotBefore    time.Time `json:"notBefore"`
	NotAfter     time.Time `json:"notAfter"`
	Authority    string    `json:"authority"`
	FailingSince time.Time `json:"failingSince,omitzero"`
	Failures     int       `json:"failures"`
	LastError    string    `json:"lastError,omitempty"`
}

// SaveCertificate keeps a domain's new certificate; its renewals are fine again.
func (s *Store) SaveCertificate(ctx context.Context, c Certificate) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO certificate (domain, chain, serial, not_before, not_after, authority)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (domain) DO UPDATE SET chain = excluded.chain, serial = excluded.serial,
		not_before = excluded.not_before, not_after = excluded.not_after, authority = excluded.authority,
		failing_since = NULL, failures = 0, last_error = ''`,
		c.Domain, string(c.Chain), c.Serial, c.NotBefore.UnixMilli(), c.NotAfter.UnixMilli(), c.Authority)
	return err
}

// Certificates are the domains' certificates.
func (s *Store) Certificates(ctx context.Context) ([]Certificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, chain, serial, not_before, not_after, authority, failing_since,
		failures, last_error FROM certificate ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Certificate{}
	for rows.Next() {
		var c Certificate
		var chain string
		var nb, na int64
		var failing sql.NullInt64
		if err := rows.Scan(&c.Domain, &chain, &c.Serial, &nb, &na, &c.Authority, &failing, &c.Failures, &c.LastError); err != nil {
			return nil, err
		}
		c.Chain, c.NotBefore, c.NotAfter = []byte(chain), time.UnixMilli(nb).UTC(), time.UnixMilli(na).UTC()
		if failing.Valid {
			c.FailingSince = time.UnixMilli(failing.Int64).UTC()
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Certificate is one domain's certificate, or ErrNotFound.
func (s *Store) Certificate(ctx context.Context, domain string) (Certificate, error) {
	all, err := s.Certificates(ctx)
	if err != nil {
		return Certificate{}, err
	}
	for _, c := range all {
		if c.Domain == domain {
			return c, nil
		}
	}
	return Certificate{}, ErrNotFound
}

// RenewalFailed records a failed renewal.
func (s *Store) RenewalFailed(ctx context.Context, domain string, at time.Time, why string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE certificate SET failures = failures + 1, last_error = ?,
		failing_since = coalesce(failing_since, ?) WHERE domain = ?`, why, at.UnixMilli(), domain)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(ErrNotFound, err)
	}
	return nil
}

// Warning is something an admin should look at.
type Warning struct {
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	What     string    `json:"what"`
	Resolved time.Time `json:"resolved,omitzero"`
}

// AddWarning raises a warning.
func (s *Store) AddWarning(ctx context.Context, w Warning) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO warning (at, source, what) VALUES (?, ?, ?)`, w.At.UnixMilli(), w.Source, w.What)
	return err
}

// OpenWarning says whether a source has this warning, and it isn't resolved.
func (s *Store) OpenWarning(ctx context.Context, source, what string) (bool, error) {
	var open bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM warning WHERE source = ? AND what = ? AND resolved_at IS NULL)`,
		source, what).Scan(&open)
	return open, err
}

// ResolveWarnings marks a source's open warnings resolved.
func (s *Store) ResolveWarnings(ctx context.Context, source string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE warning SET resolved_at = ? WHERE source = ? AND resolved_at IS NULL`, at.UnixMilli(), source)
	return err
}

// Warnings are the warnings, newest first, at most limit.
func (s *Store) Warnings(ctx context.Context, limit int) ([]Warning, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, source, what, resolved_at FROM warning ORDER BY at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Warning{}
	for rows.Next() {
		var w Warning
		var at int64
		var res sql.NullInt64
		if err := rows.Scan(&at, &w.Source, &w.What, &res); err != nil {
			return nil, err
		}
		w.At = time.UnixMilli(at).UTC()
		if res.Valid {
			w.Resolved = time.UnixMilli(res.Int64).UTC()
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
