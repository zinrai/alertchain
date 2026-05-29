// ui_mount.go registers the bundled web UI under /ui/ on the given
// mux. The UI is server-side rendered HTML plus a vendored htmx; no
// build pipeline is required.
package main

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

// mountUI registers the /ui/* routes on mux.
func mountUI(mux *http.ServeMux, store MuteStore, logger *slog.Logger, cfg UIConfig) {
	h := &uiHandler{
		store:      store,
		logger:     logger,
		userHeader: cfg.UserHeader,
		listTmpl:   mustParseUITemplate("ui/templates/layout.html", "ui/templates/list.html"),
		newTmpl:    mustParseUITemplate("ui/templates/layout.html", "ui/templates/new.html"),
		detailTmpl: mustParseUITemplate("ui/templates/layout.html", "ui/templates/detail.html"),
	}

	mux.HandleFunc("GET /ui/{$}", h.list)
	mux.HandleFunc("POST /ui/{$}", h.create)
	mux.HandleFunc("GET /ui/new", h.newForm)
	mux.HandleFunc("GET /ui/{id}", h.detail)
	mux.HandleFunc("POST /ui/{id}/expire", h.expire)

	staticFS, err := fs.Sub(uiStaticFS, "ui/static")
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
