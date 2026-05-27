// metrics.go exposes a handful of counters in the Prometheus text
// exposition format. The implementation uses only the standard library
// to avoid a direct dependency on prometheus/client_golang: alertchain
// produces a small fixed set of counters (no labels, no histograms),
// which the stdlib handles in ~30 lines.
//
// Counters are incremented via methods on *Metrics. The methods are
// nil-receiver safe so that Chain can use them unconditionally even
// when Metrics is not wired up (e.g. in unit tests that construct a
// Chain directly).
package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// Metrics holds the counters exposed via /metrics. A nil *Metrics is
// valid: all increment methods short-circuit, so tests can leave the
// field unset on Chain without panicking.
type Metrics struct {
	AlertsReceived       atomic.Uint64
	NotifySuccess        atomic.Uint64
	NotifyFailure        atomic.Uint64
	MuteLookupFailure    atomic.Uint64
	HistoryLookupFailure atomic.Uint64
	HistoryWriteFailure  atomic.Uint64
}

// NewMetrics returns a zero-valued *Metrics. Returning a pointer rather
// than a value keeps the atomic fields safely addressable.
func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) incAlertsReceived() {
	if m != nil {
		m.AlertsReceived.Add(1)
	}
}

func (m *Metrics) incNotifySuccess() {
	if m != nil {
		m.NotifySuccess.Add(1)
	}
}

func (m *Metrics) incNotifyFailure() {
	if m != nil {
		m.NotifyFailure.Add(1)
	}
}

func (m *Metrics) incMuteLookupFailure() {
	if m != nil {
		m.MuteLookupFailure.Add(1)
	}
}

func (m *Metrics) incHistoryLookupFailure() {
	if m != nil {
		m.HistoryLookupFailure.Add(1)
	}
}

func (m *Metrics) incHistoryWriteFailure() {
	if m != nil {
		m.HistoryWriteFailure.Add(1)
	}
}

// ServeHTTP writes the current counter values in the Prometheus
// exposition format. The endpoint is unauthenticated; operators that
// need access control should put a reverse proxy in front of
// alertchain (the same expectation that applies to the rest of the
// HTTP surface).
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	write := func(name, help string, v uint64) {
		fmt.Fprintf(w, "# HELP %s %s\n", name, help)
		fmt.Fprintf(w, "# TYPE %s counter\n", name)
		fmt.Fprintf(w, "%s %d\n", name, v)
	}

	write("alertchain_alerts_received_total",
		"POST /api/v2/alerts requests accepted.",
		m.AlertsReceived.Load())
	write("alertchain_notify_success_total",
		"Webhook deliveries that returned 2xx.",
		m.NotifySuccess.Load())
	write("alertchain_notify_failure_total",
		"Webhook deliveries that errored or returned non-2xx.",
		m.NotifyFailure.Load())
	write("alertchain_mute_lookup_failure_total",
		"Database errors while checking mutes.",
		m.MuteLookupFailure.Load())
	write("alertchain_history_lookup_failure_total",
		"Database errors while reading notification history.",
		m.HistoryLookupFailure.Load())
	write("alertchain_history_write_failure_total",
		"Database errors while writing notification history.",
		m.HistoryWriteFailure.Load())
}
