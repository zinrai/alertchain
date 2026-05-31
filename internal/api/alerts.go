// alerts.go implements the /api/v2/alerts endpoint.
//
// Endpoints intentionally not implemented: alertgroups, receivers,
// status.
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	ammodels "github.com/prometheus/alertmanager/api/v2/models"

	"github.com/zinrai/alertchain/internal/alertchain"
)

type alertsHandler struct {
	chain  *alertchain.Chain
	logger *slog.Logger
}

func newAlertsHandler(chain *alertchain.Chain, logger *slog.Logger) *alertsHandler {
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

// post accepts a batch of alerts and processes each. Body is a JSON
// array of postableAlert objects.
//
// A Process error (database failure) returns 500. Alerts processed
// earlier in the same batch are not rolled back; the firing-sent state
// machine deduplicates them on the sender's retry.
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
		alert := alertchain.AlertFromPostable(p)
		h.chain.Metrics.IncAlertsReceived()
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
// maintain an active-alerts cache; this endpoint always returns [].
func (h *alertsHandler) get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}
