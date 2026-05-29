// Package api assembles the HTTP routes that alertchain serves under
// /api/v2/alerts, /api/v1/mutes, /metrics, and /-/{healthy,ready}.
//
// Constructed by cmd/alertchain via NewServeMux; tests build the same
// mux to exercise the routing as production does. The UI is mounted
// separately (by cmd/alertchain calling ui.Mount on the same mux when
// the UI is enabled); this package has no knowledge of /ui/.
package api

import (
	"log/slog"
	"net/http"

	"github.com/zinrai/alertchain/internal/alertchain"
	"github.com/zinrai/alertchain/internal/store"
)

// NewServeMux returns the ServeMux that production and tests both use.
// All API-side routes (alerts, mutes, metrics, health) are registered
// here. UI routes are not; cmd/alertchain mounts those separately.
func NewServeMux(chain *alertchain.Chain, db *store.Store, metrics *alertchain.Metrics, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("/api/v2/alerts", newAlertsHandler(chain, logger))

	mutes := newMutesHandler(db, logger)
	mux.HandleFunc("/api/v1/mutes", mutes.listCreate)
	mux.HandleFunc("/api/v1/mutes/", mutes.getOrExpire)

	mux.Handle("/metrics", metrics)

	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mux
}
