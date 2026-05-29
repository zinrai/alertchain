// mute.go defines the Mute type and the MuteStore / NotificationHistory
// interfaces.
package alertchain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Mute is one mute entry.
type Mute struct {
	ID        string
	Matchers  map[string]string // all entries must equal the alert's labels
	StartsAt  time.Time
	EndsAt    time.Time
	Comment   string
	CreatedBy string
}

// Active reports whether the mute is currently in effect. The interval
// is closed at both ends: [StartsAt, EndsAt].
func (m *Mute) Active(now time.Time) bool {
	return !now.Before(m.StartsAt) && !now.After(m.EndsAt)
}

// MatchesAlert reports whether the mute applies to the given alert.
// All entries in the mute's Matchers must equal the corresponding
// labels on the alert (logical AND). An empty Matchers map matches
// any alert; in practice mute creation rejects an empty map.
func (m *Mute) MatchesAlert(alert *Alert) bool {
	return MatchAll(m.Matchers, alert.Labels)
}

// NewMuteID returns a random 16-byte hex ID. crypto/rand.Read is
// documented never to return an error on systems with a working
// /dev/urandom equivalent; if it ever does, the resulting all-zero ID
// is still a valid (if unlucky) primary key.
func NewMuteID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// MuteStore persists mutes.
type MuteStore interface {
	// Matches reports whether any active mute applies to the alert.
	Matches(ctx context.Context, alert *Alert) (bool, error)

	// List returns all mutes (active and expired) ordered by EndsAt
	// descending.
	List(ctx context.Context) ([]*Mute, error)

	// Get returns one mute by ID.
	Get(ctx context.Context, id string) (*Mute, error)

	// Create persists a new mute. The caller must set the ID.
	Create(ctx context.Context, m *Mute) error

	// Expire sets a mute's EndsAt to now, ending it immediately.
	Expire(ctx context.Context, id string) error
}

// NotificationHistory records the most recent notification attempt for
// each (rule, fingerprint) pair.
type NotificationHistory interface {
	// LastAttempt returns the recorded status of the most recent
	// attempt for the given (rule, fingerprint) pair. The exists
	// return is false if no attempt has ever been recorded.
	LastAttempt(ctx context.Context, ruleName, fingerprint string) (status NotificationStatus, exists bool, err error)

	// RecordAttempt persists the outcome of one attempt. The status
	// parameter must be one of the four NotificationStatus constants.
	RecordAttempt(ctx context.Context, ruleName, fingerprint string, at time.Time, status NotificationStatus) error
}

// MuteView is a Mute projected with its computed status (pending /
// active / expired against time.Now()).
type MuteView struct {
	ID        string            `json:"id"`
	Matchers  map[string]string `json:"matchers"`
	StartsAt  time.Time         `json:"starts_at"`
	EndsAt    time.Time         `json:"ends_at"`
	Comment   string            `json:"comment,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	Status    string            `json:"status"` // "pending" | "active" | "expired"
}

// CreateMuteRequest is the input to CreateMute, independent of wire
// format (JSON, form-encoded, ...).
type CreateMuteRequest struct {
	Matchers  map[string]string
	StartsAt  time.Time
	EndsAt    time.Time
	Comment   string
	CreatedBy string
}

// ValidationError carries Field for per-field UI rendering; Message
// is surfaced verbatim by the HTTP API.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// CreateMute validates a request, persists a new mute through store,
// and returns its projected view.
func CreateMute(ctx context.Context, store MuteStore, req CreateMuteRequest) (MuteView, error) {
	if err := validateMuteCreate(req); err != nil {
		return MuteView{}, err
	}
	m := &Mute{
		ID:        NewMuteID(),
		Matchers:  req.Matchers,
		StartsAt:  req.StartsAt.UTC(),
		EndsAt:    req.EndsAt.UTC(),
		Comment:   req.Comment,
		CreatedBy: req.CreatedBy,
	}
	if err := store.Create(ctx, m); err != nil {
		return MuteView{}, err
	}
	return projectMuteView(m, time.Now().UTC()), nil
}

// ListMutes returns all stored mutes projected against a single
// "now" snapshot, so the status field is consistent across the slice.
func ListMutes(ctx context.Context, store MuteStore) ([]MuteView, error) {
	mutes, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]MuteView, 0, len(mutes))
	for _, m := range mutes {
		out = append(out, projectMuteView(m, now))
	}
	return out, nil
}

// GetMute returns one mute by id, projected with status.
func GetMute(ctx context.Context, store MuteStore, id string) (MuteView, error) {
	m, err := store.Get(ctx, id)
	if err != nil {
		return MuteView{}, err
	}
	return projectMuteView(m, time.Now().UTC()), nil
}

func validateMuteCreate(req CreateMuteRequest) error {
	if len(req.Matchers) == 0 {
		return &ValidationError{Field: "matchers", Message: "matchers must be non-empty"}
	}
	if req.StartsAt.IsZero() || req.EndsAt.IsZero() {
		field := "starts_at"
		if !req.StartsAt.IsZero() {
			field = "ends_at"
		}
		return &ValidationError{Field: field, Message: "starts_at and ends_at are required"}
	}
	if !req.EndsAt.After(req.StartsAt) {
		return &ValidationError{Field: "ends_at", Message: "ends_at must be after starts_at"}
	}
	if strings.TrimSpace(req.Comment) == "" {
		return &ValidationError{Field: "comment", Message: "comment is required"}
	}
	if strings.TrimSpace(req.CreatedBy) == "" {
		return &ValidationError{Field: "created_by", Message: "created_by is required"}
	}
	return nil
}

func projectMuteView(m *Mute, now time.Time) MuteView {
	var status string
	switch {
	case now.Before(m.StartsAt):
		status = "pending"
	case now.After(m.EndsAt):
		status = "expired"
	default:
		status = "active"
	}
	return MuteView{
		ID:        m.ID,
		Matchers:  m.Matchers,
		StartsAt:  m.StartsAt,
		EndsAt:    m.EndsAt,
		Comment:   m.Comment,
		CreatedBy: m.CreatedBy,
		Status:    status,
	}
}
