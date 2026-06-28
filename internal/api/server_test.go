package api

// server_test.go exercises the HTTP server end-to-end against a real
// PostgreSQL instance. These tests build the same ServeMux that
// production uses (via newServeMux) and drive it through httptest,
// so any routing change in server.go is exercised here without
// duplicate test-side wiring.
//
// The tests verify properties that only emerge when the chain
// evaluation, the database state machine, and the HTTP layer work
// together: concurrency safety, mute interaction, fan-out, and
// catch-all ordering. They use the real DB rather than fakes
// because the behavior under test is, in part, the interaction
// between alertchain's Process logic and PostgreSQL's
// serialization guarantees.
//
// Setup: set DATABASE_URL to a PostgreSQL instance with migrations
// already applied. Tests skip when DATABASE_URL is unset. Suite-level
// DB lifecycle (advisory lock + TRUNCATE) is in main_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	ammodels "github.com/prometheus/alertmanager/api/v2/models"

	"github.com/zinrai/alertchain/internal/alertchain"
	"github.com/zinrai/alertchain/internal/store"
)

// receiverProbe is a fake webhook destination that counts and records
// incoming requests. One probe is started per test; multiple probes
// can run in parallel within a fan-out test.
type receiverProbe struct {
	URL    string
	hits   atomic.Int64
	server *httptest.Server
}

func newReceiverProbe() *receiverProbe {
	p := &receiverProbe{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	p.URL = p.server.URL
	return p
}

func (p *receiverProbe) Hits() int64 { return p.hits.Load() }
func (p *receiverProbe) Close()      { p.server.Close() }

// serverHarness wires a real PostgreSQL store, an in-process Chain,
// and an httptest server built from newServeMux, the same function
// production uses. The harness exposes the base URL that tests POST
// against.
type serverHarness struct {
	t     *testing.T
	store *store.Store
	chain *alertchain.Chain
	srv   *httptest.Server
	URL   string
}

func newServerHarness(t *testing.T, receivers map[string]*alertchain.Receiver, rules []*alertchain.Rule) *serverHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}

	db, err := store.OpenStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := alertchain.NewMetrics()

	chain := &alertchain.Chain{
		Receivers: receivers,
		Rules:     rules,
		Mutes:     db,
		History:   db,
		Alerts:    db,
		Notifier:  alertchain.NewHTTPNotifier(),
		Logger:    logger,
		Metrics:   metrics,
	}

	srv := httptest.NewServer(NewServeMux(chain, db, metrics, logger))

	h := &serverHarness{
		t:     t,
		store: db,
		chain: chain,
		srv:   srv,
		URL:   srv.URL,
	}
	t.Cleanup(h.close)
	return h
}

func (h *serverHarness) close() {
	h.srv.Close()
	h.store.Close()
}

// postAlert sends a single PostableAlert to /api/v2/alerts and asserts
// the server returned 200.
func postAlert(t *testing.T, baseURL string, labels map[string]string) {
	t.Helper()
	payload := ammodels.PostableAlerts{
		{
			Annotations: ammodels.LabelSet{},
			StartsAt:    strfmt.DateTime(time.Now().UTC()),
			Alert: ammodels.Alert{
				Labels: ammodels.LabelSet(labels),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal alert: %v", err)
	}
	resp, err := http.Post(baseURL+"/api/v2/alerts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST alerts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		t.Fatalf("POST alerts: status %d: %s", resp.StatusCode, string(excerpt))
	}
}

// postMute creates a mute via /api/v1/mutes and returns its id. The
// payload is alertchain-native: matchers is a label-name to
// expected-value map, and times are RFC3339.
func postMute(t *testing.T, baseURL string, matchers map[string]string, duration time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	payload := map[string]any{
		"matchers":   matchers,
		"starts_at":  now,
		"ends_at":    now.Add(duration),
		"comment":    "server test",
		"created_by": "server_test",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal mute: %v", err)
	}
	resp, err := http.Post(baseURL+"/api/v1/mutes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST mute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		t.Fatalf("POST mute: status %d: %s", resp.StatusCode, string(excerpt))
	}
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatalf("decode mute reply: %v", err)
	}
	return reply.ID
}

// ---------------------------------------------------------------------
// Proposition A: distinct alerts are all delivered
// ---------------------------------------------------------------------

// TestServerDistinctAlertsAllDelivered sends N alerts with distinct
// fingerprints concurrently and verifies every one reaches the
// receiver exactly once. This is the most fundamental server
// correctness property: nothing is lost.
func TestServerDistinctAlertsAllDelivered(t *testing.T) {
	probe := newReceiverProbe()
	defer probe.Close()

	receivers := map[string]*alertchain.Receiver{
		"main": {Name: "main", Type: "webhook", URL: probe.URL},
	}
	rules := []*alertchain.Rule{
		{
			Name:     "all",
			Match:    nil, // catch-all
			Receiver: "main",
		},
	}
	h := newServerHarness(t, receivers, rules)

	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			postAlert(t, h.URL, map[string]string{
				"alertname": "BulkTest",
				"instance":  fmt.Sprintf("host-%03d", i),
			})
		}(i)
	}
	wg.Wait()

	if got := probe.Hits(); got != N {
		t.Errorf("expected exactly %d webhook deliveries, got %d", N, got)
	}
}

// ---------------------------------------------------------------------
// Proposition B: same alert concurrent does not vanish
// ---------------------------------------------------------------------

// TestServerSameAlertConcurrent sends N copies of the same alert
// (identical fingerprint) concurrently and verifies the webhook is
// called at least once and at most N times. Duplicates within this
// range are acceptable per the design contract (webhook receivers
// must dedup by fingerprint).
func TestServerSameAlertConcurrent(t *testing.T) {
	probe := newReceiverProbe()
	defer probe.Close()

	receivers := map[string]*alertchain.Receiver{
		"main": {Name: "main", Type: "webhook", URL: probe.URL},
	}
	rules := []*alertchain.Rule{
		{Name: "all", Match: nil, Receiver: "main"},
	}
	h := newServerHarness(t, receivers, rules)

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postAlert(t, h.URL, map[string]string{
				"alertname": "Hammer",
				"instance":  "host-singleton",
			})
		}()
	}
	wg.Wait()

	got := probe.Hits()
	if got < 1 {
		t.Errorf("expected at least 1 webhook delivery, got %d", got)
	}
	if got > N {
		t.Errorf("webhook deliveries exceeded send count: got %d, sent %d", got, N)
	}
	t.Logf("same-alert concurrent delivery count: %d (1 <= n <= %d)", got, N)
}

// ---------------------------------------------------------------------
// Proposition C: fan-out to multiple receivers via continue:true
// ---------------------------------------------------------------------

// TestServerFanoutToMultipleReceivers verifies that an alert matching
// rules with continue:true reaches all targeted receivers, not just
// the first.
func TestServerFanoutToMultipleReceivers(t *testing.T) {
	probeA := newReceiverProbe()
	probeB := newReceiverProbe()
	defer probeA.Close()
	defer probeB.Close()

	receivers := map[string]*alertchain.Receiver{
		"recvA": {Name: "recvA", Type: "webhook", URL: probeA.URL},
		"recvB": {Name: "recvB", Type: "webhook", URL: probeB.URL},
	}
	rules := []*alertchain.Rule{
		{
			Name:     "to-a",
			Match:    map[string]string{"severity": "critical"},
			Receiver: "recvA",
			Continue: true,
		},
		{
			Name:     "to-b",
			Match:    map[string]string{"severity": "critical"},
			Receiver: "recvB",
		},
	}
	h := newServerHarness(t, receivers, rules)

	postAlert(t, h.URL, map[string]string{
		"alertname": "Fanout",
		"severity":  "critical",
	})

	if got := probeA.Hits(); got != 1 {
		t.Errorf("recvA: expected 1 hit, got %d", got)
	}
	if got := probeB.Hits(); got != 1 {
		t.Errorf("recvB: expected 1 hit, got %d", got)
	}
}

// ---------------------------------------------------------------------
// Proposition D: mute takes effect after creation
// ---------------------------------------------------------------------

// TestServerMuteTakesEffect creates a mute matching a specific label
// set, then sends alerts both with and without that label, and verifies
// only the matching alerts are muted.
//
// Only alerts sent AFTER the mute POST completes are asserted; the
// test does not race against mute creation.
func TestServerMuteTakesEffect(t *testing.T) {
	probe := newReceiverProbe()
	defer probe.Close()

	receivers := map[string]*alertchain.Receiver{
		"main": {Name: "main", Type: "webhook", URL: probe.URL},
	}
	rules := []*alertchain.Rule{
		{Name: "all", Match: nil, Receiver: "main"},
	}
	h := newServerHarness(t, receivers, rules)

	// Create mute for severity=info first, and wait for it to be
	// persisted (the POST returns after the DB commit).
	postMute(t, h.URL, map[string]string{"severity": "info"}, 1*time.Hour)

	// Now send alerts. The mute is durably in place.
	postAlert(t, h.URL, map[string]string{
		"alertname": "Quiet",
		"severity":  "info", // should be muted
	})
	postAlert(t, h.URL, map[string]string{
		"alertname": "Loud",
		"severity":  "critical", // should pass through
	})

	if got := probe.Hits(); got != 1 {
		t.Errorf("expected exactly 1 webhook delivery (the critical one), got %d", got)
	}
}

// ---------------------------------------------------------------------
// Proposition E: catch-all + ordering correctness
// ---------------------------------------------------------------------

// TestServerCatchAllAndOrdering exercises two related properties at
// once:
//
//  1. An alert not matching any specific rule reaches the catch-all
//     and is delivered to the catch-all's receiver (here a low-priority
//     log sink), never lost.
//  2. An alert matching a specific earlier rule is delivered there
//     and does not fall through to the catch-all.
//
// We verify by counting deliveries to two receivers: the critical
// alert must land exactly once on main and never on the sink; the
// trivial alert must land exactly once on the sink and never on main.
// If rules were evaluated in the wrong order, the catch-all would
// catch everything and main would see zero hits (proving the failure
// mode).
func TestServerCatchAllAndOrdering(t *testing.T) {
	main := newReceiverProbe()
	defer main.Close()
	sink := newReceiverProbe()
	defer sink.Close()

	receivers := map[string]*alertchain.Receiver{
		"main":     {Name: "main", Type: "webhook", URL: main.URL},
		"log-sink": {Name: "log-sink", Type: "webhook", URL: sink.URL},
	}
	rules := []*alertchain.Rule{
		{
			Name:     "critical-only",
			Match:    map[string]string{"severity": "critical"},
			Receiver: "main",
		},
		{
			Name:     "catch-all",
			Match:    nil, // catch-all
			Receiver: "log-sink",
		},
	}
	h := newServerHarness(t, receivers, rules)

	// Matches first rule -> delivered to main.
	postAlert(t, h.URL, map[string]string{
		"alertname": "Important",
		"severity":  "critical",
	})
	// Matches catch-all -> delivered to the log sink.
	postAlert(t, h.URL, map[string]string{
		"alertname": "Trivial",
		"severity":  "debug",
	})

	if got := main.Hits(); got != 1 {
		t.Errorf("expected exactly 1 delivery to main (critical), got %d", got)
	}
	if got := sink.Hits(); got != 1 {
		t.Errorf("expected exactly 1 delivery to log sink (trivial), got %d", got)
	}
}
