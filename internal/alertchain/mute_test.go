package alertchain

import (
	"errors"
	"testing"
	"time"
)

func TestMuteActive(t *testing.T) {
	now := time.Now().UTC()
	m := &Mute{
		StartsAt: now.Add(-1 * time.Hour),
		EndsAt:   now.Add(1 * time.Hour),
	}
	if !m.Active(now) {
		t.Errorf("mute should be active")
	}
	if m.Active(now.Add(2 * time.Hour)) {
		t.Errorf("mute should be expired")
	}
	if m.Active(now.Add(-2 * time.Hour)) {
		t.Errorf("mute should not yet be active")
	}
}

// TestMuteActiveBoundary verifies that Active uses the closed interval
// [StartsAt, EndsAt], the same boundary semantics Alertmanager uses
// for silences.
func TestMuteActiveBoundary(t *testing.T) {
	t0 := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	m := &Mute{
		StartsAt: t0,
		EndsAt:   t0.Add(1 * time.Hour),
	}
	if !m.Active(t0) {
		t.Errorf("Active at exactly StartsAt must be true")
	}
	if !m.Active(t0.Add(1 * time.Hour)) {
		t.Errorf("Active at exactly EndsAt must be true (closed interval)")
	}
	if m.Active(t0.Add(1*time.Hour + time.Nanosecond)) {
		t.Errorf("Active just after EndsAt must be false")
	}
	if m.Active(t0.Add(-time.Nanosecond)) {
		t.Errorf("Active just before StartsAt must be false")
	}
}

func TestMuteMatchesAlert(t *testing.T) {
	m := &Mute{
		Matchers: map[string]string{
			"severity": "info",
			"team":     "infra",
		},
	}
	// All match.
	if !m.MatchesAlert(&Alert{Labels: map[string]string{
		"severity": "info",
		"team":     "infra",
		"extra":    "x",
	}}) {
		t.Errorf("mute should match alert with all required labels")
	}
	// One mismatched.
	if m.MatchesAlert(&Alert{Labels: map[string]string{
		"severity": "info",
		"team":     "platform",
	}}) {
		t.Errorf("mute should not match when team differs")
	}
	// Missing label.
	if m.MatchesAlert(&Alert{Labels: map[string]string{
		"severity": "info",
	}}) {
		t.Errorf("mute should not match when team label is absent")
	}
}

// TestValidateMuteCreate exercises validateMuteCreate as a pure
// function. The test does not touch a store.
func TestValidateMuteCreate(t *testing.T) {
	now := time.Now().UTC()
	base := CreateMuteRequest{
		Matchers:  map[string]string{"severity": "info"},
		StartsAt:  now,
		EndsAt:    now.Add(1 * time.Hour),
		Comment:   "c",
		CreatedBy: "u",
	}
	cases := []struct {
		name      string
		mutate    func(*CreateMuteRequest)
		wantField string // "" means expect no error
	}{
		{"valid", func(r *CreateMuteRequest) {}, ""},
		{"empty matchers", func(r *CreateMuteRequest) { r.Matchers = map[string]string{} }, "matchers"},
		{"nil matchers", func(r *CreateMuteRequest) { r.Matchers = nil }, "matchers"},
		{"zero starts_at", func(r *CreateMuteRequest) { r.StartsAt = time.Time{} }, "starts_at"},
		{"zero ends_at", func(r *CreateMuteRequest) { r.EndsAt = time.Time{} }, "ends_at"},
		{"ends before starts", func(r *CreateMuteRequest) { r.EndsAt = r.StartsAt.Add(-1 * time.Hour) }, "ends_at"},
		{"empty comment", func(r *CreateMuteRequest) { r.Comment = "" }, "comment"},
		{"whitespace-only comment", func(r *CreateMuteRequest) { r.Comment = "   " }, "comment"},
		{"empty created_by", func(r *CreateMuteRequest) { r.CreatedBy = "" }, "created_by"},
		{"whitespace-only created_by", func(r *CreateMuteRequest) { r.CreatedBy = "   " }, "created_by"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mutate(&req)
			err := validateMuteCreate(req)
			if c.wantField == "" {
				if err != nil {
					t.Errorf("got err %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("got err %v, want *ValidationError", err)
			}
			if ve.Field != c.wantField {
				t.Errorf("Field: got %q, want %q", ve.Field, c.wantField)
			}
		})
	}
}

// TestProjectMuteView checks status computation at and around the
// [StartsAt, EndsAt] closed-interval boundaries.
func TestProjectMuteView(t *testing.T) {
	t0 := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	m := &Mute{
		ID:       "m1",
		Matchers: map[string]string{"x": "y"},
		StartsAt: t0,
		EndsAt:   t0.Add(1 * time.Hour),
	}
	cases := []struct {
		name   string
		now    time.Time
		status string
	}{
		{"before starts: pending", t0.Add(-time.Nanosecond), "pending"},
		{"at starts: active (closed interval)", t0, "active"},
		{"middle: active", t0.Add(30 * time.Minute), "active"},
		{"at ends: active (closed interval)", t0.Add(1 * time.Hour), "active"},
		{"after ends: expired", t0.Add(1*time.Hour + time.Nanosecond), "expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := projectMuteView(m, c.now)
			if v.Status != c.status {
				t.Errorf("Status: got %q, want %q", v.Status, c.status)
			}
			if v.ID != m.ID || v.Matchers["x"] != "y" {
				t.Errorf("projection corrupted Mute fields: %+v", v)
			}
		})
	}
}
