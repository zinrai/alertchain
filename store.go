// store.go is the PostgreSQL implementation of MuteStore and
// NotificationHistory.
//
// This file contains only DML (SELECT, INSERT, UPDATE, DELETE). The
// schema itself is not managed here: see migrations/ in the repository
// root for the SQL migration files, which are applied by the standard
// golang-migrate CLI (or any equivalent tool) as a separate operation
// before alertchain is started. The rationale for keeping migration
// out of the application binary is in DESIGN.md under "Schema
// management is separated from the application".
//
// HA is delegated to PostgreSQL (managed services, streaming
// replication, etc.). alertchain itself does not implement clustering
// or gossip; the second invariant in DESIGN.md is "state lives in one
// place" and that one place is the database.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store wraps a *sql.DB and implements both MuteStore and
// NotificationHistory.
type Store struct {
	db *sql.DB
}

// OpenStore opens a PostgreSQL connection using the given DSN. Both
// URL form (postgres://user:pass@host/db) and key/value form
// (host=... user=... dbname=...) are accepted.
//
// The schema must already exist when OpenStore is called. alertchain
// itself does not create tables; that is the responsibility of an
// out-of-band migration step. A sanity check at the end of this
// function verifies that the expected tables are present so that a
// misconfigured deployment fails fast at startup, rather than at the
// first runtime query.
func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s := &Store{db: db}
	if err := s.checkSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// checkSchema verifies that the tables alertchain reads from and
// writes to are present. The check is intentionally narrow: it does
// not validate column types or indexes (the migration tool is
// responsible for those). The goal is only to detect the common
// misconfiguration "migrations have not been applied" and surface it
// at startup with a clear error.
func (s *Store) checkSchema(ctx context.Context) error {
	for _, table := range []string{"mutes", "notifications"} {
		// LIMIT 0 returns no rows but still parses and plans the
		// query, which fails with SQLSTATE 42P01 if the relation is
		// absent. This is cheaper than a SELECT COUNT(*) and does not
		// scan the table.
		q := fmt.Sprintf(`SELECT 1 FROM %s LIMIT 0`, table)
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("required table %q is missing (have schema migrations been applied?): %w", table, err)
		}
	}
	return nil
}

// Matches implements MuteStore. It loads currently active mutes and
// checks each against the alert. A linear scan over the active set is
// fast enough for the expected scale (typically only a handful of
// mutes are active at once; mute is an operator-driven feature).
//
// The "active" predicate uses the closed interval [starts_at, ends_at]
// to match Alertmanager's silence boundary semantics in
// silence/silence.go::getState. Operators familiar with Alertmanager
// see no surprise at the boundary.
func (s *Store) Matches(ctx context.Context, alert *Alert) (bool, error) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT matchers FROM mutes WHERE starts_at <= $1 AND ends_at >= $1`,
		now)
	if err != nil {
		return false, fmt.Errorf("query active mutes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, fmt.Errorf("scan mute: %w", err)
		}
		var matchers map[string]string
		if err := json.Unmarshal(raw, &matchers); err != nil {
			return false, fmt.Errorf("decode mute matchers: %w", err)
		}
		if matchAll(matchers, alert.Labels) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// List implements MuteStore.
func (s *Store) List(ctx context.Context) ([]*Mute, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, matchers, starts_at, ends_at, comment, created_by
		 FROM mutes ORDER BY ends_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list mutes: %w", err)
	}
	defer rows.Close()

	var result []*Mute
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// Get implements MuteStore. Returns an error when not found.
func (s *Store) Get(ctx context.Context, id string) (*Mute, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, matchers, starts_at, ends_at, comment, created_by
		 FROM mutes WHERE id = $1`, id)
	m, err := scanMute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("mute %q not found", id)
	}
	return m, err
}

// Create implements MuteStore.
func (s *Store) Create(ctx context.Context, m *Mute) error {
	matchersJSON, err := json.Marshal(m.Matchers)
	if err != nil {
		return fmt.Errorf("encode matchers: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mutes (id, matchers, starts_at, ends_at, comment, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.ID,
		matchersJSON,
		m.StartsAt.UTC(),
		m.EndsAt.UTC(),
		m.Comment,
		m.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("insert mute: %w", err)
	}
	return nil
}

// Expire implements MuteStore by setting ends_at to now. Returns an
// error if no row was affected.
func (s *Store) Expire(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE mutes SET ends_at = $1 WHERE id = $2`, now, id)
	if err != nil {
		return fmt.Errorf("expire mute: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("mute %q not found", id)
	}
	return nil
}

// LastAttempt implements NotificationHistory.
func (s *Store) LastAttempt(ctx context.Context, ruleName, fingerprint string) (NotificationStatus, bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM notifications WHERE rule_name = $1 AND fingerprint = $2`,
		ruleName, fingerprint).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query last_attempt: %w", err)
	}
	return NotificationStatus(status), true, nil
}

// RecordAttempt implements NotificationHistory.
func (s *Store) RecordAttempt(ctx context.Context, ruleName, fingerprint string, at time.Time, status NotificationStatus) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notifications (rule_name, fingerprint, sent_at, status)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (rule_name, fingerprint) DO UPDATE
		 SET sent_at = EXCLUDED.sent_at, status = EXCLUDED.status`,
		ruleName, fingerprint, at.UTC(), string(status))
	if err != nil {
		return fmt.Errorf("record notification: %w", err)
	}
	return nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMute(r rowScanner) (*Mute, error) {
	var (
		id                 string
		matchersJSON       []byte
		startsAt, endsAt   time.Time
		comment, createdBy sql.NullString
	)
	if err := r.Scan(&id, &matchersJSON, &startsAt, &endsAt, &comment, &createdBy); err != nil {
		return nil, err // pass through sql.ErrNoRows for callers to detect
	}
	var matchers map[string]string
	if err := json.Unmarshal(matchersJSON, &matchers); err != nil {
		return nil, fmt.Errorf("decode matchers: %w", err)
	}
	return &Mute{
		ID:        id,
		Matchers:  matchers,
		StartsAt:  startsAt,
		EndsAt:    endsAt,
		Comment:   comment.String,
		CreatedBy: createdBy.String,
	}, nil
}
