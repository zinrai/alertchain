// store.go is the PostgreSQL implementation of MuteStore and
// NotificationHistory. DML only; schema is managed by migrations/
// applied out-of-band before startup.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/zinrai/alertchain/internal/alertchain"
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
// The schema must already exist; OpenStore does not create tables.
// A sanity check verifies the expected tables are present so a
// misconfigured deployment fails at startup rather than at the first
// runtime query.
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

// checkSchema verifies the required tables are present. The check is
// intentionally narrow: it does not validate column types or indexes
// (the migration tool owns those).
func (s *Store) checkSchema(ctx context.Context) error {
	for _, table := range []string{"mutes", "notifications", "alerts"} {
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

// Matches implements MuteStore. Linear scan over the active set;
// typically only a handful of mutes are active at once.
func (s *Store) Matches(ctx context.Context, alert *alertchain.Alert) (bool, error) {
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
		if alertchain.MatchAll(matchers, alert.Labels) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// List implements MuteStore. The MutesPresent / MutesExpired filter
// uses the closed-interval boundary: ends_at == now is still present.
func (s *Store) List(ctx context.Context, filter alertchain.MuteFilter) ([]*alertchain.Mute, error) {
	var args []any
	where := ""
	switch filter.Status {
	case alertchain.MutesPresent:
		args = append(args, time.Now().UTC())
		where = "WHERE ends_at >= $1"
	case alertchain.MutesExpired:
		args = append(args, time.Now().UTC())
		where = "WHERE ends_at < $1"
	}
	q := fmt.Sprintf(`SELECT id, matchers, starts_at, ends_at, comment, created_by
		FROM mutes %s ORDER BY ends_at DESC`, where)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list mutes: %w", err)
	}
	defer rows.Close()

	var result []*alertchain.Mute
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
func (s *Store) Get(ctx context.Context, id string) (*alertchain.Mute, error) {
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
func (s *Store) Create(ctx context.Context, m *alertchain.Mute) error {
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
func (s *Store) LastAttempt(ctx context.Context, ruleName, fingerprint string) (alertchain.NotificationStatus, bool, error) {
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
	return alertchain.NotificationStatus(status), true, nil
}

// RecordAttempt implements NotificationHistory.
func (s *Store) RecordAttempt(ctx context.Context, ruleName, fingerprint string, at time.Time, status alertchain.NotificationStatus) error {
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

func scanMute(r rowScanner) (*alertchain.Mute, error) {
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
	return &alertchain.Mute{
		ID:        id,
		Matchers:  matchers,
		StartsAt:  startsAt,
		EndsAt:    endsAt,
		Comment:   comment.String,
		CreatedBy: createdBy.String,
	}, nil
}

// UpsertAlert implements alertchain.AlertStore. Annotations and
// generatorURL on the Alert struct are not persisted; they are
// presentation content the webhook destination renders.
func (s *Store) UpsertAlert(ctx context.Context, alert *alertchain.Alert, now time.Time) error {
	labelsJSON, err := json.Marshal(alert.Labels)
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	var endsAt sql.NullTime
	if !alert.EndsAt.IsZero() {
		endsAt = sql.NullTime{Time: alert.EndsAt.UTC(), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO alerts
			(fingerprint, labels, starts_at, ends_at, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (fingerprint) DO UPDATE SET
			labels = EXCLUDED.labels,
			ends_at = EXCLUDED.ends_at,
			last_seen_at = EXCLUDED.last_seen_at
	`, alert.Fingerprint(), labelsJSON, alert.StartsAt.UTC(), endsAt, now.UTC())
	if err != nil {
		return fmt.Errorf("upsert alert: %w", err)
	}
	return nil
}

const defaultAlertLimit = 100

// alertSelectCols is the column list scanAlertRow expects.
const alertSelectCols = `fingerprint, labels, starts_at, ends_at, first_seen_at, last_seen_at`

// ListAlerts implements alertchain.AlertStore.
func (s *Store) ListAlerts(ctx context.Context, filter alertchain.AlertFilter) ([]alertchain.AlertView, error) {
	now := time.Now().UTC()
	var args []any
	conds := []string{}

	switch filter.Status {
	case alertchain.AlertsFiring:
		args = append(args, now)
		conds = append(conds, fmt.Sprintf("(alerts.ends_at IS NULL OR alerts.ends_at > $%d)", len(args)))
	case alertchain.AlertsExpired:
		args = append(args, now)
		conds = append(conds, fmt.Sprintf("(alerts.ends_at IS NOT NULL AND alerts.ends_at <= $%d)", len(args)))
	}

	if filter.ExcludeMuted {
		args = append(args, now)
		// JSONB containment per row, evaluated against currently
		// active mutes. The GIN index on alerts.labels is not used
		// here because we are matching alerts.labels @> mutes.matchers
		// (alerts is the LHS), but the active-mute set is expected to
		// be small (low dozens) so the cost is bounded.
		conds = append(conds, fmt.Sprintf(`NOT EXISTS (
			SELECT 1 FROM mutes m
			WHERE m.starts_at <= $%d AND m.ends_at >= $%d
			  AND alerts.labels @> m.matchers
		)`, len(args), len(args)))
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAlertLimit
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT %s FROM alerts %s ORDER BY last_seen_at DESC LIMIT $%d`,
		alertSelectCols, where, len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	return scanAlertRows(rows, now)
}

// GetAlert implements alertchain.AlertStore. Returns an error when
// the fingerprint is not found.
func (s *Store) GetAlert(ctx context.Context, fingerprint string) (alertchain.AlertView, error) {
	now := time.Now().UTC()
	row := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM alerts WHERE fingerprint = $1`, alertSelectCols),
		fingerprint)
	v, err := scanAlertRow(row, now)
	if errors.Is(err, sql.ErrNoRows) {
		return alertchain.AlertView{}, fmt.Errorf("alert %q not found", fingerprint)
	}
	return v, err
}

// MatchingAlerts implements alertchain.AlertStore.
//
// The matchers map is JSON-encoded and passed to PostgreSQL's JSONB
// containment operator (@>). An empty matchers map matches every
// alert; the mute API rejects empty matchers on creation, so callers
// from the UI should not arrive here with one.
func (s *Store) MatchingAlerts(ctx context.Context, matchers map[string]string, filter alertchain.AlertFilter) ([]alertchain.AlertView, error) {
	matchersJSON, err := json.Marshal(matchers)
	if err != nil {
		return nil, fmt.Errorf("encode matchers: %w", err)
	}
	now := time.Now().UTC()
	where := "WHERE labels @> $1::jsonb"
	args := []any{matchersJSON}
	switch filter.Status {
	case alertchain.AlertsFiring:
		args = append(args, now)
		where += fmt.Sprintf(" AND (ends_at IS NULL OR ends_at > $%d)", len(args))
	case alertchain.AlertsExpired:
		args = append(args, now)
		where += fmt.Sprintf(" AND ends_at IS NOT NULL AND ends_at <= $%d", len(args))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAlertLimit
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT %s FROM alerts %s ORDER BY last_seen_at DESC LIMIT $%d`,
		alertSelectCols, where, len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("matching alerts: %w", err)
	}
	defer rows.Close()

	return scanAlertRows(rows, now)
}

func scanAlertRows(rows *sql.Rows, now time.Time) ([]alertchain.AlertView, error) {
	var out []alertchain.AlertView
	for rows.Next() {
		v, err := scanAlertRow(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanAlertRow(r rowScanner, now time.Time) (alertchain.AlertView, error) {
	var (
		fingerprint             string
		labelsJSON              []byte
		startsAt                time.Time
		endsAt                  sql.NullTime
		firstSeenAt, lastSeenAt time.Time
	)
	if err := r.Scan(&fingerprint, &labelsJSON, &startsAt, &endsAt, &firstSeenAt, &lastSeenAt); err != nil {
		return alertchain.AlertView{}, err
	}
	var labels map[string]string
	if err := json.Unmarshal(labelsJSON, &labels); err != nil {
		return alertchain.AlertView{}, fmt.Errorf("decode labels: %w", err)
	}
	view := alertchain.AlertView{
		Fingerprint: fingerprint,
		Labels:      labels,
		StartsAt:    startsAt,
		FirstSeenAt: firstSeenAt,
		LastSeenAt:  lastSeenAt,
	}
	if endsAt.Valid {
		view.EndsAt = endsAt.Time
	}
	view.Status = alertchain.DeriveAlertStatus(view.EndsAt, now)
	return view, nil
}
