// Package ui registers the bundled web UI under /ui/ on a given mux.
// The UI is server-side rendered HTML plus a vendored htmx; no build
// pipeline is required.
package ui

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/zinrai/alertchain/internal/alertchain"
)

// Store is the persistence surface the UI handlers need. It is the
// union of the mute and alert lifecycle stores; *store.Store from
// internal/store implements both.
type Store interface {
	alertchain.MuteStore
	alertchain.AlertStore
}

// Mount registers the /ui/* routes on mux. The store argument is used
// for both mute lifecycle calls (CreateMute / ListMutes / GetMute /
// store.Expire) and alert observability (ListAlerts / GetAlert /
// MatchingAlerts).
func Mount(mux *http.ServeMux, store Store, logger *slog.Logger, cfg alertchain.UIConfig) {
	h := &uiHandler{
		store:          store,
		logger:         logger,
		userHeader:     cfg.UserHeader,
		alertsListTmpl: mustParseUITemplate("templates/layout.html", "templates/alerts_list.html"),
		muteListTmpl:   mustParseUITemplate("templates/layout.html", "templates/mute_list.html"),
		muteNewTmpl:    mustParseUITemplate("templates/layout.html", "templates/mute_new.html"),
	}

	// Alerts (root view = "what's happening"). All info is inlined on
	// the list page; there is no per-alert detail route.
	mux.HandleFunc("GET /ui/{$}", h.alertsList)

	// Mutes. Each mute's matching alerts are inlined on the list
	// page; there is no per-mute detail route.
	mux.HandleFunc("GET /ui/mutes/{$}", h.mutesList)
	mux.HandleFunc("POST /ui/mutes/{$}", h.muteCreate)
	mux.HandleFunc("GET /ui/mutes/new", h.muteNewForm)
	mux.HandleFunc("POST /ui/mutes/{id}/expire", h.muteExpire)

	// Root redirect.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	staticFS, err := fs.Sub(uiStaticFS, "static")
	if err != nil {
		panic("ui: sub static fs: " + err.Error())
	}
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticFS))))
}

func mustParseUITemplate(files ...string) *template.Template {
	t, err := template.ParseFS(uiTemplatesFS, files...)
	if err != nil {
		panic("ui: parse templates: " + err.Error())
	}
	return t
}
