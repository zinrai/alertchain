// handlers.go implements the /ui/* HTTP handlers.
//
// The UI calls the same lifecycle functions (alertchain.CreateMute,
// ListMutes, GetMute) as the HTTP API, so validation and status
// computation are identical between the two surfaces.
package ui

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zinrai/alertchain/internal/alertchain"
)

// DTLayout is the format produced and consumed by <input
// type="datetime-local">. The value is interpreted as UTC (the JS in
// time-sync.js renders UTC components into the field). Exported so
// tests in other packages can construct form values.
const DTLayout = "2006-01-02T15:04"

type uiHandler struct {
	store      alertchain.MuteStore
	logger     *slog.Logger
	userHeader string
	listTmpl   *template.Template
	newTmpl    *template.Template
	detailTmpl *template.Template
}

// list renders GET /ui/.
func (h *uiHandler) list(w http.ResponseWriter, r *http.Request) {
	views, err := alertchain.ListMutes(r.Context(), h.store)
	if err != nil {
		http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, h.listTmpl, map[string]any{"Mutes": views})
}

// newForm renders GET /ui/new with prefill from match.* and comment
// query parameters and from the configured user header.
func (h *uiHandler) newForm(w http.ResponseWriter, r *http.Request) {
	matchers := matchersFromQuery(r.URL.Query())
	comment := r.URL.Query().Get("comment")
	createdBy := h.userFromHeader(r)
	h.renderNew(w, newFormData{
		Matchers:  matchers,
		Comment:   comment,
		CreatedBy: createdBy,
		StartsAt:  time.Now().UTC().Format(DTLayout),
		Duration:  "1h",
		EndsAt:    time.Now().UTC().Add(time.Hour).Format(DTLayout),
	})
}

// create handles POST /ui/.
func (h *uiHandler) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	matchers := matchersFromForm(r.Form)
	startsAtStr := r.FormValue("starts_at")
	endsAtStr := r.FormValue("ends_at")
	duration := r.FormValue("duration")
	comment := r.FormValue("comment")
	createdBy := r.FormValue("created_by")
	if strings.TrimSpace(createdBy) == "" {
		createdBy = h.userFromHeader(r)
	}

	starts, _ := time.Parse(DTLayout, startsAtStr)
	ends, _ := time.Parse(DTLayout, endsAtStr)

	_, err := alertchain.CreateMute(r.Context(), h.store, alertchain.CreateMuteRequest{
		Matchers:  matchers,
		StartsAt:  starts,
		EndsAt:    ends,
		Comment:   comment,
		CreatedBy: createdBy,
	})
	if err != nil {
		var ve *alertchain.ValidationError
		if errors.As(err, &ve) {
			h.renderNew(w, newFormData{
				Matchers:  matchers,
				Comment:   comment,
				CreatedBy: createdBy,
				StartsAt:  startsAtStr,
				Duration:  duration,
				EndsAt:    endsAtStr,
				Error:     map[string]string{ve.Field: ve.Message},
			})
			return
		}
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// detail renders GET /ui/{id}.
func (h *uiHandler) detail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, err := alertchain.GetMute(r.Context(), h.store, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, h.detailTmpl, map[string]any{"Mute": view})
}

// expire handles POST /ui/{id}/expire. For htmx requests it returns
// an empty body so hx-swap="outerHTML" removes the row; otherwise it
// 303-redirects to the list.
func (h *uiHandler) expire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Expire(r.Context(), id); err != nil {
		http.Error(w, "expire: "+err.Error(), http.StatusNotFound)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// newFormData is the template data for the create form, also used
// for re-rendering after a validation error.
type newFormData struct {
	Matchers  map[string]string
	Comment   string
	CreatedBy string
	StartsAt  string
	Duration  string
	EndsAt    string
	Error     map[string]string
}

func (h *uiHandler) renderNew(w http.ResponseWriter, data newFormData) {
	if data.Error == nil {
		data.Error = map[string]string{}
	}
	h.render(w, h.newTmpl, data)
}

func (h *uiHandler) render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("ui template render failed", "err", err)
	}
}

func (h *uiHandler) userFromHeader(r *http.Request) string {
	if h.userHeader == "" {
		return ""
	}
	return r.Header.Get(h.userHeader)
}

// matchersFromQuery extracts match.<name>=<value> query parameters.
// Used by GET /ui/new for the Slack→UI prefill flow described in
// alertchain-ui-integration-design.md §6.
func matchersFromQuery(q url.Values) map[string]string {
	out := map[string]string{}
	for k, vs := range q {
		if strings.HasPrefix(k, "match.") && len(vs) > 0 {
			name := strings.TrimPrefix(k, "match.")
			if name != "" {
				out[name] = vs[0]
			}
		}
	}
	return out
}

// matchersFromForm pairs match-name[i] with match-value[i] from the
// posted form, skipping rows where the name is empty after trimming.
func matchersFromForm(form url.Values) map[string]string {
	names := form["match-name"]
	values := form["match-value"]
	out := map[string]string{}
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || i >= len(values) {
			continue
		}
		out[name] = values[i]
	}
	return out
}
