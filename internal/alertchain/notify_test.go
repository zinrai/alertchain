package alertchain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWebhookPayloadIncludesFingerprintAndStatus verifies the webhook
// payload follows the alertchain notification contract: each alert
// carries a `fingerprint` and a per-alert `status`, enabling the
// receiver to deduplicate by fingerprint.
func TestWebhookPayloadIncludesFingerprintAndStatus(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewHTTPNotifier()
	recv := &Receiver{Name: "test-webhook", Type: "webhook", URL: server.URL}
	alert := &Alert{
		Labels:   map[string]string{"alertname": "DiskFull", "severity": "critical"},
		StartsAt: time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
	}

	if err := notifier.Notify(context.Background(), recv, alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var payload webhookPayload
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Receiver != "test-webhook" {
		t.Errorf("receiver: got %q, want %q", payload.Receiver, "test-webhook")
	}
	if payload.Status != "firing" {
		t.Errorf("top-level status: got %q, want %q", payload.Status, "firing")
	}
	if len(payload.Alerts) != 1 {
		t.Fatalf("alerts: got %d entries, want 1", len(payload.Alerts))
	}
	a := payload.Alerts[0]
	if a.Status != "firing" {
		t.Errorf("alert status: got %q, want %q", a.Status, "firing")
	}
	if a.Fingerprint == "" {
		t.Errorf("alert fingerprint must not be empty")
	}
	if a.Fingerprint != alert.Fingerprint() {
		t.Errorf("alert fingerprint: got %q, want %q (stable across calls)",
			a.Fingerprint, alert.Fingerprint())
	}
}

// TestWebhookPayloadStatusResolved verifies that an alert with a past
// EndsAt is reported as resolved at the per-alert level.
func TestWebhookPayloadStatusResolved(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewHTTPNotifier()
	recv := &Receiver{Name: "test", Type: "webhook", URL: server.URL}
	alert := &Alert{
		Labels:   map[string]string{"alertname": "DiskFull"},
		StartsAt: time.Now().Add(-2 * time.Hour),
		EndsAt:   time.Now().Add(-1 * time.Hour),
	}

	if err := notifier.Notify(context.Background(), recv, alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var payload webhookPayload
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Alerts[0].Status != "resolved" {
		t.Errorf("alert status: got %q, want resolved", payload.Alerts[0].Status)
	}
}

// TestWebhookFingerprintStableAcrossInstances verifies that two Alert
// objects with the same labels produce identical fingerprints in the
// webhook payload, the property webhook receivers rely on for dedup.
func TestWebhookFingerprintStableAcrossInstances(t *testing.T) {
	a1 := newWebhookAlert(&Alert{
		Labels: map[string]string{"alertname": "X", "team": "infra"},
	})
	a2 := newWebhookAlert(&Alert{
		Labels: map[string]string{"team": "infra", "alertname": "X"},
	})
	if a1.Fingerprint != a2.Fingerprint {
		t.Errorf("fingerprint must be label-order independent, got %q vs %q",
			a1.Fingerprint, a2.Fingerprint)
	}
}

// TestNotifyDiscardIsNoop verifies that the built-in discard type
// short-circuits to no-op even if Notify is invoked directly (defense
// in depth; the chain Process also short-circuits earlier).
func TestNotifyDiscardIsNoop(t *testing.T) {
	notifier := NewHTTPNotifier()
	recv := &Receiver{Name: "discard", Type: "discard"}
	alert := &Alert{Labels: map[string]string{"alertname": "X"}}
	if err := notifier.Notify(context.Background(), recv, alert); err != nil {
		t.Errorf("discard Notify should not error, got %v", err)
	}
}

// TestNotifyContextDeadline verifies that the context deadline supplied
// by the caller actually bounds the request. The handler sleeps longer
// than the context allows; Notify must return before the handler
// finishes.
func TestNotifyContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			// Client cancelled; just return.
		}
	}))
	defer server.Close()

	notifier := NewHTTPNotifier()
	recv := &Receiver{Name: "slow", Type: "webhook", URL: server.URL}
	alert := &Alert{Labels: map[string]string{"x": "1"}}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := notifier.Notify(ctx, recv, alert)
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("expected error from cancelled context")
	}
	if elapsed > time.Second {
		t.Errorf("Notify took %v, expected to return promptly after context cancel", elapsed)
	}
}
