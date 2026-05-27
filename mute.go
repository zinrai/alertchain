// mute.go defines the mute model and the MuteStore interface.
//
// A Mute suppresses any alert whose labels match every entry in the
// mute's Matchers map. Mutes are time-bounded: a mute is active only
// when StartsAt <= now <= EndsAt.
//
// Mutes are alertchain's own concept; they are not the Alertmanager
// silence type and do not share its wire format. See DESIGN.md for
// the reasoning behind keeping them separate.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Mute represents one mute entry in the internal representation.
type Mute struct {
	ID        string
	Matchers  map[string]string // all entries must equal the alert's labels
	StartsAt  time.Time
	EndsAt    time.Time
	Comment   string
	CreatedBy string
}

// Active reports whether the mute is currently in effect.
//
// The interval is closed at both ends: [StartsAt, EndsAt]. This is
// the same boundary semantics Alertmanager applies to silences;
// alertchain mirrors it so that operators familiar with one system
// experience no surprise at boundaries.
func (m *Mute) Active(now time.Time) bool {
	return !now.Before(m.StartsAt) && !now.After(m.EndsAt)
}

// MatchesAlert reports whether the mute applies to the given alert.
// All entries in the mute's Matchers must equal the corresponding
// labels on the alert (logical AND). An empty Matchers map matches
// any alert; in practice mute creation rejects an empty map.
func (m *Mute) MatchesAlert(alert *Alert) bool {
	return matchAll(m.Matchers, alert.Labels)
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

// MuteStore persists mutes. The interface is small enough that a
// caller could swap PostgreSQL for a test fake without ceremony.
//
// The method name Matches (rather than Mutes) avoids a confusing
// noun/verb collision with the type name.
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
// each (rule, fingerprint) pair, including which firing/resolved state
// was attempted and whether it succeeded.
//
// The four recorded statuses (firing-sent, firing-failed,
// resolved-sent, resolved-failed) drive Process's decision to skip or
// re-deliver: a prior success of the same status causes skip, while
// any other recorded value (including the opposite firing/resolved
// state and the failed variants) causes another delivery attempt.
type NotificationHistory interface {
	// LastAttempt returns the recorded status of the most recent
	// attempt for the given (rule, fingerprint) pair. The exists
	// return is false if no attempt has ever been recorded.
	LastAttempt(ctx context.Context, ruleName, fingerprint string) (status NotificationStatus, exists bool, err error)

	// RecordAttempt persists the outcome of one attempt. The status
	// parameter must be one of the four NotificationStatus constants.
	RecordAttempt(ctx context.Context, ruleName, fingerprint string, at time.Time, status NotificationStatus) error
}
