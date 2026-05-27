// api_alerts.go implements the alerts endpoints of the HTTP API.
//
// Wire format follows Alertmanager v2 exactly because the type
// definitions are imported from
// github.com/prometheus/alertmanager/api/v2/models. This guarantees
// that Prometheus, vmalert, promxy, and other clients of the
// Alertmanager v2 API can send alerts to alertchain without changes.
//
// Endpoints intentionally not implemented: alertgroups, receivers,
// status. These expose Alertmanager internal concepts (aggregation
// groups, route tree) that alertchain does not have.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	ammodels "github.com/prometheus/alertmanager/api/v2/models"
)

type alertsHandler struct {
	chain  *Chain
	logger *slog.Logger
}

func newAlertsHandler(chain *Chain, logger *slog.Logger) *alertsHandler {
	return &alertsHandler{chain: chain, logger: logger}
}

func (h *alertsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.post(w, r)
	case http.MethodGet:
		h.get(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// post accepts a batch of alerts and processes each. Per Alertmanager
// convention the body is a JSON array of postableAlert objects.
//
// A Process error indicates a database failure that prevented the
// alert from being evaluated. The handler returns 500 in that case so
// that the sender (Prometheus, vmalert, etc.) retries on its next
// push. Alerts processed earlier in the same batch are not rolled
// back; the firing-sent state machine deduplicates them on the retry.
func (h *alertsHandler) post(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var postable ammodels.PostableAlerts
	if err := json.Unmarshal(body, &postable); err != nil {
		http.Error(w, "parse alerts: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, p := range postable {
		alert := alertFromPostable(p)
		h.chain.Metrics.incAlertsReceived()
		if err := h.chain.Process(r.Context(), alert); err != nil {
			h.logger.Error("process alert failed",
				"fingerprint", alert.Fingerprint(), "err", err)
			http.Error(w, "process: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// get returns the currently active alerts. alertchain does not
// maintain a long-term active alerts cache, so this endpoint returns
// an empty array. Clients that need the active set should query
// Prometheus directly.
func (h *alertsHandler) get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}
