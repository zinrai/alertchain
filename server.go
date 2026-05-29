// server.go assembles the HTTP routes that alertchain serves.
//
// Separated from cmdServe so tests can build a server with the same
// route table as production without going through the CLI entry point.
// Lifecycle concerns (signals, graceful shutdown, listen address)
// remain in cmdServe.
package main

import (
	"log/slog"
	"net/http"
)

// newServeMux returns the ServeMux that production and tests both use.
// All routes that alertchain exposes are registered here.
func newServeMux(chain *Chain, store *Store, metrics *Metrics, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("/api/v2/alerts", newAlertsHandler(chain, logger))

	mutes := newMutesHandler(store, logger)
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
