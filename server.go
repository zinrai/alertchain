// server.go assembles the HTTP routes that alertchain serves.
//
// Separated from cmdServe so tests can build a server with the same
// route table as production without going through the CLI entry point.
// Lifecycle concerns (signals, graceful shutdown, listen address)
// remain in cmdServe.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
)

// newServeMux returns the ServeMux that production and tests both use.
// All routes that alertchain exposes are registered here.
func newServeMux(chain *Chain, store *Store, metrics *Metrics, logger *slog.Logger, uiCfg UIConfig) *http.ServeMux {
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

	if uiCfg.Enabled {
		mountUI(mux, store, logger, uiCfg)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "alertchain")
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Endpoints:")
			fmt.Fprintln(w, "  POST /api/v2/alerts")
			fmt.Fprintln(w, "  GET/POST /api/v1/mutes  GET/DELETE /api/v1/mutes/{id}")
			fmt.Fprintln(w, "  GET /metrics")
			fmt.Fprintln(w, "  GET /-/healthy  GET /-/ready")
		})
	}

	return mux
}
