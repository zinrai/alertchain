package main

import (
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
