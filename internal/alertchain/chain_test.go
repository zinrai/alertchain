package alertchain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestMatchAllEmptyMatchesAnything(t *testing.T) {
	if !MatchAll(nil, map[string]string{"any": "value"}) {
		t.Errorf("nil conditions should match anything")
	}
	if !MatchAll(map[string]string{}, map[string]string{"any": "value"}) {
		t.Errorf("empty conditions should match anything")
	}
}

func TestMatchAllAllEntriesMustEqual(t *testing.T) {
	conds := map[string]string{"severity": "critical", "team": "infra"}

	if !MatchAll(conds, map[string]string{"severity": "critical", "team": "infra", "extra": "x"}) {
		t.Errorf("alert with all required labels plus extras should match")
	}
	if MatchAll(conds, map[string]string{"severity": "critical", "team": "platform"}) {
		t.Errorf("differing team should not match")
	}
	if MatchAll(conds, map[string]string{"severity": "critical"}) {
		t.Errorf("missing team should not match (empty string != \"infra\")")
	}
}

func TestEvaluateStopOnFirst(t *testing.T) {
	c := &Chain{
		Rules: []*Rule{
			{Name: "r1", Match: map[string]string{"x": "1"}, Receiver: "a"},
			{Name: "r2", Match: map[string]string{"x": "1"}, Receiver: "b"},
		},
	}
	alert := &Alert{Labels: map[string]string{"x": "1"}}
	decisions := c.Evaluate(alert)
	if len(decisions) != 1 || decisions[0].Rule.Name != "r1" {
		t.Errorf("expected single decision for r1, got %v", decisions)
	}
}

func TestEvaluateContinue(t *testing.T) {
	c := &Chain{
		Rules: []*Rule{
			{Name: "r1", Match: map[string]string{"x": "1"}, Receiver: "a", Continue: true},
			{Name: "r2", Match: map[string]string{"x": "1"}, Receiver: "b"},
			{Name: "r3", Match: map[string]string{"x": "1"}, Receiver: "c"},
		},
	}
	alert := &Alert{Labels: map[string]string{"x": "1"}}
	decisions := c.Evaluate(alert)
	// r1 has Continue, r2 does not, so r3 is never reached.
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}
	if decisions[0].Rule.Name != "r1" || decisions[1].Rule.Name != "r2" {
		t.Errorf("unexpected rule order: %s, %s",
			decisions[0].Rule.Name, decisions[1].Rule.Name)
	}
}

func TestEvaluateCatchAll(t *testing.T) {
	c := &Chain{
		Rules: []*Rule{
			{Name: "specific", Match: map[string]string{"x": "1"}, Receiver: "a"},
			{Name: "catch", Match: nil, Receiver: "default"},
		},
	}
	alert := &Alert{Labels: map[string]string{"x": "2"}}
	decisions := c.Evaluate(alert)
	if len(decisions) != 1 || decisions[0].Rule.Name != "catch" {
		t.Errorf("expected catch-all to match, got %v", decisions)
	}
}

func TestEvaluateNoMatch(t *testing.T) {
	c := &Chain{
		Rules: []*Rule{
			{Name: "r1", Match: map[string]string{"x": "1"}, Receiver: "a"},
		},
	}
	alert := &Alert{Labels: map[string]string{"x": "2"}}
	decisions := c.Evaluate(alert)
	if len(decisions) != 0 {
		t.Errorf("expected no decisions, got %v", decisions)
	}
}

func TestFingerprintStable(t *testing.T) {
	a1 := &Alert{Labels: map[string]string{"a": "1", "b": "2"}}
	a2 := &Alert{Labels: map[string]string{"b": "2", "a": "1"}}
	if a1.Fingerprint() != a2.Fingerprint() {
		t.Errorf("fingerprint must be order-independent")
	}
}

func TestFingerprintDiffers(t *testing.T) {
	a1 := &Alert{Labels: map[string]string{"a": "1"}}
	a2 := &Alert{Labels: map[string]string{"a": "2"}}
	if a1.Fingerprint() == a2.Fingerprint() {
		t.Errorf("fingerprint must differ for different label values")
	}
}

// TestDesiredStatusBoundary verifies that desiredStatus uses the closed
// interval [-inf, now] for resolution (i.e. EndsAt == now is resolved).
// This matches Mute.Active's closed-interval boundary.
func TestDesiredStatusBoundary(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	zero := &Alert{}
	if got := desiredStatus(zero, now); got != StatusFiringSent {
		t.Errorf("zero EndsAt: got %q, want %q", got, StatusFiringSent)
	}

	atNow := &Alert{EndsAt: now}
	if got := desiredStatus(atNow, now); got != StatusResolvedSent {
		t.Errorf("EndsAt == now should be resolved (closed interval), got %q", got)
	}

	past := &Alert{EndsAt: now.Add(-time.Second)}
	if got := desiredStatus(past, now); got != StatusResolvedSent {
		t.Errorf("EndsAt in past: got %q, want %q", got, StatusResolvedSent)
	}

	future := &Alert{EndsAt: now.Add(time.Second)}
	if got := desiredStatus(future, now); got != StatusFiringSent {
		t.Errorf("EndsAt in future: got %q, want %q", got, StatusFiringSent)
	}
}

// --- fakes for Process testing -----------------------------------------

type fakeMutes struct {
	muted bool
	err   error
}

func (f *fakeMutes) Matches(ctx context.Context, a *Alert) (bool, error) {
	return f.muted, f.err
}
func (f *fakeMutes) List(ctx context.Context, _ MuteFilter) ([]*Mute, error) {
	return nil, nil
}
func (f *fakeMutes) Get(ctx context.Context, id string) (*Mute, error) { return nil, nil }
func (f *fakeMutes) Create(ctx context.Context, m *Mute) error         { return nil }
func (f *fakeMutes) Expire(ctx context.Context, id string) error       { return nil }

type historyRecord struct {
	at     time.Time
	status NotificationStatus
}

type fakeHistory struct {
	mu      sync.Mutex
	records map[string]historyRecord
}

func newFakeHistory() *fakeHistory {
	return &fakeHistory{records: map[string]historyRecord{}}
}

func (f *fakeHistory) key(rule, fp string) string { return rule + "\x00" + fp }

func (f *fakeHistory) LastAttempt(ctx context.Context, rule, fp string) (NotificationStatus, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.records[f.key(rule, fp)]
	if !ok {
		return "", false, nil
	}
	return r.status, true, nil
}

func (f *fakeHistory) RecordAttempt(ctx context.Context, rule, fp string, at time.Time, status NotificationStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[f.key(rule, fp)] = historyRecord{at: at, status: status}
	return nil
}

// errHistory always fails LastAttempt. Used to verify that history
// lookup failure aborts Process.
type errHistory struct{}

func (errHistory) LastAttempt(ctx context.Context, rule, fp string) (NotificationStatus, bool, error) {
	return "", false, errors.New("history db down")
}
func (errHistory) RecordAttempt(ctx context.Context, rule, fp string, at time.Time, s NotificationStatus) error {
	return nil
}

type fakeNotifier struct {
	mu       sync.Mutex
	calls    int
	failNext bool
}

func (f *fakeNotifier) Notify(ctx context.Context, recv *Receiver, a *Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNext {
		f.failNext = false
		return errors.New("simulated webhook failure")
	}
	return nil
}

func newTestChain(t *testing.T) (*Chain, *fakeHistory, *fakeNotifier) {
	t.Helper()
	hist := newFakeHistory()
	notif := &fakeNotifier{}
	c := &Chain{
		Receivers: map[string]*Receiver{
			"webhook": {Name: "webhook", Type: "webhook", URL: "http://x"},
		},
		Rules: []*Rule{
			{Name: "rule1", Match: map[string]string{"severity": "critical"}, Receiver: "webhook"},
		},
		Mutes:    &fakeMutes{},
		History:  hist,
		Notifier: notif,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return c, hist, notif
}

// --- tests --------------------------------------------------------------

// TestProcessFiringInitialDelivery verifies the basic firing path:
// no prior record, alert is delivered, status becomes firing-sent.
func TestProcessFiringInitialDelivery(t *testing.T) {
	c, hist, notif := newTestChain(t)
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if notif.calls != 1 {
		t.Errorf("expected 1 webhook call, got %d", notif.calls)
	}
	status, ok, _ := hist.LastAttempt(context.Background(), "rule1", alert.Fingerprint())
	if !ok || status != StatusFiringSent {
		t.Errorf("expected status %q, got %q (exists=%v)", StatusFiringSent, status, ok)
	}
}

// TestProcessFiringDuplicateSkipped verifies the steady-state: when
// firing-sent is already recorded, the next firing alert with the
// same fingerprint is silently skipped. This is what makes
// alertchain a one-shot router rather than a reminder.
func TestProcessFiringDuplicateSkipped(t *testing.T) {
	c, hist, notif := newTestChain(t)
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}
	fp := alert.Fingerprint()

	if err := hist.RecordAttempt(context.Background(), "rule1", fp,
		time.Now().Add(-1*time.Minute), StatusFiringSent); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if notif.calls != 0 {
		t.Errorf("expected 0 webhook calls for duplicate firing, got %d", notif.calls)
	}
}

// TestProcessFiringFailureRetried verifies that a recorded
// firing-failed causes the next firing alert to be re-delivered,
// recovering once the webhook returns 2xx.
func TestProcessFiringFailureRetried(t *testing.T) {
	c, hist, notif := newTestChain(t)
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}
	fp := alert.Fingerprint()

	if err := hist.RecordAttempt(context.Background(), "rule1", fp,
		time.Now().Add(-1*time.Minute), StatusFiringFailed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if notif.calls != 1 {
		t.Errorf("expected 1 webhook call after prior failure, got %d", notif.calls)
	}
	status, _, _ := hist.LastAttempt(context.Background(), "rule1", fp)
	if status != StatusFiringSent {
		t.Errorf("expected status %q after recovery, got %q", StatusFiringSent, status)
	}
}

// TestProcessFailureRecordsFiringFailed verifies that when the webhook
// errors during a firing attempt, the status is recorded as
// firing-failed (not just generic failed).
func TestProcessFailureRecordsFiringFailed(t *testing.T) {
	c, hist, notif := newTestChain(t)
	notif.failNext = true
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if notif.calls != 1 {
		t.Errorf("expected 1 webhook call attempt, got %d", notif.calls)
	}
	status, ok, _ := hist.LastAttempt(context.Background(), "rule1", alert.Fingerprint())
	if !ok || status != StatusFiringFailed {
		t.Errorf("expected %q, got %q (exists=%v)", StatusFiringFailed, status, ok)
	}
}

// TestProcessResolvedAfterFiringDelivered verifies the firing->resolved
// transition: an alert with EndsAt in the past triggers a delivery
// even when the prior status is firing-sent. The new status becomes
// resolved-sent.
func TestProcessResolvedAfterFiringDelivered(t *testing.T) {
	c, hist, notif := newTestChain(t)
	alert := &Alert{
		Labels: map[string]string{"severity": "critical"},
		EndsAt: time.Now().Add(-1 * time.Hour),
	}
	fp := alert.Fingerprint()

	if err := hist.RecordAttempt(context.Background(), "rule1", fp,
		time.Now().Add(-2*time.Hour), StatusFiringSent); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if notif.calls != 1 {
		t.Errorf("expected 1 webhook call for resolved transition, got %d", notif.calls)
	}
	status, _, _ := hist.LastAttempt(context.Background(), "rule1", fp)
	if status != StatusResolvedSent {
		t.Errorf("expected status %q, got %q", StatusResolvedSent, status)
	}
}

// TestProcessMuteFailureReturnsError verifies the contract that mute
// lookup errors propagate from Process and no notification is sent.
func TestProcessMuteFailureReturnsError(t *testing.T) {
	c, _, notif := newTestChain(t)
	c.Mutes = &fakeMutes{err: errors.New("db down")}
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}

	err := c.Process(context.Background(), alert)
	if err == nil {
		t.Errorf("expected error from Process when mute lookup fails")
	}
	if notif.calls != 0 {
		t.Errorf("expected no webhook call when mute DB fails, got %d", notif.calls)
	}
}

// TestProcessHistoryFailureReturnsError verifies that history lookup
// errors propagate from Process and no notification is sent. This is
// the symmetric counterpart of TestProcessMuteFailureReturnsError.
func TestProcessHistoryFailureReturnsError(t *testing.T) {
	c, _, notif := newTestChain(t)
	c.History = errHistory{}
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}

	err := c.Process(context.Background(), alert)
	if err == nil {
		t.Errorf("expected error from Process when history lookup fails")
	}
	if notif.calls != 0 {
		t.Errorf("expected no webhook call when history DB fails, got %d", notif.calls)
	}
}

// TestProcessMetricsNilSafe verifies that Process does not panic when
// Chain.Metrics is nil. Unit tests construct Chain directly and
// typically leave Metrics unset; production wires it via main.go.
func TestProcessMetricsNilSafe(t *testing.T) {
	c, _, _ := newTestChain(t)
	c.Metrics = nil
	alert := &Alert{Labels: map[string]string{"severity": "critical"}}

	if err := c.Process(context.Background(), alert); err != nil {
		t.Errorf("Process with nil Metrics: %v", err)
	}
}
