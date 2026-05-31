// alert_store.go defines the persistence contract for observed
// alerts and the lifecycle functions presentation layers call.
//
// The HTTP API and UI both go through UpsertAlert / ListAlerts /
// GetAlert / MatchingAlerts; no presentation layer touches AlertStore
// directly. This mirrors the mute lifecycle pattern from mute.go.
package alertchain

import (
	"context"
	"time"
)

// AlertView is the presentation type for one observed alert. It
// carries the routing-relevant fields alertchain persists, plus the
// derived Status (firing/expired) so the UI does not have to
// recompute it.
//
// Annotations and generatorURL are deliberately not included: those
// are presentation content for the downstream notification
// destination (Slack, PagerDuty), not alertchain's responsibility.
// The webhook payload path uses Alert.Annotations / Alert.GeneratorURL
// directly from the incoming payload, so notification destinations
// still receive everything they need.
type AlertView struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	StartsAt    time.Time         `json:"starts_at"`
	EndsAt      time.Time         `json:"ends_at,omitempty"` // zero = firing
	FirstSeenAt time.Time         `json:"first_seen_at"`
	LastSeenAt  time.Time         `json:"last_seen_at"`
	Status      string            `json:"status"` // "firing" | "expired"
}

// AlertFilter selects which alerts to return.
type AlertFilter struct {
	Status AlertStatusFilter
	Limit  int // 0 means use the default

	// ExcludeMuted, when true, omits alerts whose labels are matched
	// by any currently active mute (starts_at <= now <= ends_at). The
	// UI's "Firing" tab sets this so operators see only alerts that
	// need their attention. Muted alerts remain visible under their
	// respective mute on the mutes page.
	ExcludeMuted bool
}

// AlertStatusFilter narrows the result set by firing/expired status.
type AlertStatusFilter string

const (
	AlertsAll     AlertStatusFilter = ""        // any status
	AlertsFiring  AlertStatusFilter = "firing"  // ends_at is null or in the future
	AlertsExpired AlertStatusFilter = "expired" // ends_at is past
)

// AlertStore persists observed alerts. *store.Store implements it.
type AlertStore interface {
	// UpsertAlert inserts or updates the alert keyed by its
	// fingerprint, recording labels, annotations, and timing.
	// first_seen_at is preserved on update; last_seen_at is set to now.
	UpsertAlert(ctx context.Context, alert *Alert, now time.Time) error

	// ListAlerts returns recent alerts, filtered by status. Ordered
	// by last_seen_at descending.
	ListAlerts(ctx context.Context, filter AlertFilter) ([]AlertView, error)

	// GetAlert returns one alert by fingerprint.
	GetAlert(ctx context.Context, fingerprint string) (AlertView, error)

	// MatchingAlerts returns alerts whose labels satisfy every
	// (name, value) pair in matchers (JSONB containment). The filter
	// further narrows by firing/expired status.
	MatchingAlerts(ctx context.Context, matchers map[string]string, filter AlertFilter) ([]AlertView, error)
}

// UpsertAlert records an incoming alert in the store. Both
// presentation layers (HTTP and UI) call this indirectly via Process;
// it is exported so the cmd wiring layer can verify the store
// implements it.
func UpsertAlert(ctx context.Context, store AlertStore, alert *Alert) error {
	return store.UpsertAlert(ctx, alert, time.Now().UTC())
}

// ListAlerts returns alerts filtered by status; the result already
// has the derived Status field populated.
func ListAlerts(ctx context.Context, store AlertStore, filter AlertFilter) ([]AlertView, error) {
	return store.ListAlerts(ctx, filter)
}

// GetAlert returns one alert by fingerprint.
func GetAlert(ctx context.Context, store AlertStore, fingerprint string) (AlertView, error) {
	return store.GetAlert(ctx, fingerprint)
}

// MatchingAlerts returns the alerts that satisfy the matchers used
// by the given mute.
func MatchingAlerts(ctx context.Context, store AlertStore, matchers map[string]string, filter AlertFilter) ([]AlertView, error) {
	return store.MatchingAlerts(ctx, matchers, filter)
}

// DeriveAlertStatus reports "firing" or "expired" for the given
// EndsAt value, using the same closed-interval boundary as
// alertStatus and desiredStatus (EndsAt <= now is expired).
func DeriveAlertStatus(endsAt time.Time, now time.Time) string {
	if !endsAt.IsZero() && !endsAt.After(now) {
		return "expired"
	}
	return "firing"
}
