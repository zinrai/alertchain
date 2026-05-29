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

// Mount registers the /ui/* routes on mux. The store argument is used
// for mute persistence; the handlers call alertchain's lifecycle
// functions (CreateMute / ListMutes / GetMute) through this store.
func Mount(mux *http.ServeMux, store alertchain.MuteStore, logger *slog.Logger, cfg alertchain.UIConfig) {
	h := &uiHandler{
		store:      store,
		logger:     logger,
		userHeader: cfg.UserHeader,
		listTmpl:   mustParseUITemplate("templates/layout.html", "templates/list.html"),
		newTmpl:    mustParseUITemplate("templates/layout.html", "templates/new.html"),
		detailTmpl: mustParseUITemplate("templates/layout.html", "templates/detail.html"),
	}

	mux.HandleFunc("GET /ui/{$}", h.list)
	mux.HandleFunc("POST /ui/{$}", h.create)
	mux.HandleFunc("GET /ui/new", h.newForm)
	mux.HandleFunc("GET /ui/{id}", h.detail)
	mux.HandleFunc("POST /ui/{id}/expire", h.expire)
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
