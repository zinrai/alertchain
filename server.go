// server.go assembles the HTTP routes that alertchain serves.
//
// The HTTP mux construction is intentionally separated from cmdServe in
// main.go so that tests can build a server with the same routing as
// production without going through the CLI entry point. Without this
// split, tests would need to re-implement the route table, creating a
// silent drift risk whenever a new endpoint is added.
//
// Lifecycle concerns (signal handling, graceful shutdown, listen
// address) remain in cmdServe; this file only describes what the
// server responds to.
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
