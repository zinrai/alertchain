// handlers.go implements the /ui/* HTTP handlers.
//
// Mute lifecycle handlers go through CreateMute / ListMutes /
// store.Expire. Alert observability is read-only via ListAlerts and
// MatchingAlerts. The same shared store backs both surfaces, so /ui/
// (alerts) and /ui/mutes/ render against the same data as
// /api/v1/mutes and /api/v2/alerts. There are no per-alert or
// per-mute detail pages: each list page inlines everything the
// operator needs.
package ui

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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
	store          Store
	logger         *slog.Logger
	userHeader     string
	alertsListTmpl *template.Template
	muteListTmpl   *template.Template
	muteNewTmpl    *template.Template
}

// ---------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------

// alertsList renders GET /ui/. ?status=expired filters to expired;
// otherwise firing. The Firing tab additionally excludes alerts
// matched by any currently active mute, so the list shows only what
// needs an operator's attention. Muted alerts remain visible under
// their respective mute on /ui/mutes/. Expired alerts are not
// filtered by mute status (the sender has resolved them, so the
// mute is irrelevant).
func (h *uiHandler) alertsList(w http.ResponseWriter, r *http.Request) {
	filter := alertchain.AlertFilter{
		Status:       alertchain.AlertsFiring,
		ExcludeMuted: true,
	}
	statusStr := "firing"
	if r.URL.Query().Get("status") == "expired" {
		filter.Status = alertchain.AlertsExpired
		filter.ExcludeMuted = false
		statusStr = "expired"
	}
	alerts, err := alertchain.ListAlerts(r.Context(), h.store, filter)
	if err != nil {
		http.Error(w, "list alerts: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, h.alertsListTmpl, map[string]any{
		"Nav":    "alerts",
		"Status": statusStr,
		"Alerts": alerts,
	})
}

// ---------------------------------------------------------------------
// Mutes
// ---------------------------------------------------------------------

// muteEntry pairs one mute with the alerts whose labels currently
// satisfy its matchers. The list template renders one entry per
// mute, with the matches inlined under each.
type muteEntry struct {
	Mute    alertchain.MuteView
	Matches []alertchain.AlertView
}

// mutesList renders GET /ui/mutes/. ?status=expired shows the
// historical set; otherwise the default is the present-tense set
// (active + pending). alertchain has no mute retention, so without
// the default filter the page would eventually drown in expired
// entries unrelated to current suppression.
//
// For each present mute it also performs a JSONB containment query
// for the firing alerts currently matching, so the page is a
// single-screen overview of "what is this mute affecting right now".
// Matching is restricted to firing alerts for the same retention
// reason. The matches query is skipped for expired mutes (their
// "currently matching" set is meaningless).
//
// N+1 queries here are acceptable: the mute count is expected to be
// in the low dozens, and each query is bounded by the JSONB GIN
// index on alerts.labels.
func (h *uiHandler) mutesList(w http.ResponseWriter, r *http.Request) {
	filter := alertchain.MuteFilter{Status: alertchain.MutesPresent}
	statusStr := "present"
	if r.URL.Query().Get("status") == "expired" {
		filter.Status = alertchain.MutesExpired
		statusStr = "expired"
	}
	mutes, err := alertchain.ListMutes(r.Context(), h.store, filter)
	if err != nil {
		http.Error(w, "list mutes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	entries := make([]muteEntry, 0, len(mutes))
	for _, m := range mutes {
		var matches []alertchain.AlertView
		if m.Status != "expired" {
			matches, err = alertchain.MatchingAlerts(r.Context(), h.store, m.Matchers,
				alertchain.AlertFilter{Status: alertchain.AlertsFiring})
			if err != nil {
				h.logger.Error("matching alerts failed", "mute_id", m.ID, "err", err)
				matches = nil
			}
		}
		entries = append(entries, muteEntry{Mute: m, Matches: matches})
	}
	h.render(w, h.muteListTmpl, map[string]any{
		"Nav":     "mutes",
		"Status":  statusStr,
		"Entries": entries,
	})
}

// muteNewForm renders GET /ui/mutes/new. The matchers textarea is
// pre-filled from match.<name>=<value> query parameters when present
// (the Slack→UI prefill flow); the operator can then edit the text
// in place before submitting.
func (h *uiHandler) muteNewForm(w http.ResponseWriter, r *http.Request) {
	matchersText := matchersTextFromQuery(r.URL.Query())
	comment := r.URL.Query().Get("comment")
	createdBy := h.userFromHeader(r)
	h.renderNew(w, newFormData{
		Nav:          "mutes",
		MatchersText: matchersText,
		Comment:      comment,
		CreatedBy:    createdBy,
		StartsAt:     time.Now().UTC().Format(DTLayout),
		Duration:     "1h",
		EndsAt:       time.Now().UTC().Add(time.Hour).Format(DTLayout),
	})
}

// muteCreate handles POST /ui/mutes/.
func (h *uiHandler) muteCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	matchersText := r.FormValue("matchers-text")
	startsAtStr := r.FormValue("starts_at")
	endsAtStr := r.FormValue("ends_at")
	duration := r.FormValue("duration")
	comment := r.FormValue("comment")
	createdBy := r.FormValue("created_by")
	if strings.TrimSpace(createdBy) == "" {
		createdBy = h.userFromHeader(r)
	}

	matchers, parseErr := parseMatchersText(matchersText)
	if parseErr != nil {
		h.renderNew(w, newFormData{
			Nav:          "mutes",
			MatchersText: matchersText,
			Comment:      comment,
			CreatedBy:    createdBy,
			StartsAt:     startsAtStr,
			Duration:     duration,
			EndsAt:       endsAtStr,
			Error:        map[string]string{"matchers": parseErr.Error()},
		})
		return
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
				Nav:          "mutes",
				MatchersText: matchersText,
				Comment:      comment,
				CreatedBy:    createdBy,
				StartsAt:     startsAtStr,
				Duration:     duration,
				EndsAt:       endsAtStr,
				Error:        map[string]string{ve.Field: ve.Message},
			})
			return
		}
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/mutes/", http.StatusSeeOther)
}

// muteExpire handles POST /ui/mutes/{id}/expire. For htmx requests it
// returns an empty body so hx-swap="outerHTML" removes the row;
// otherwise it 303-redirects to the mute list.
func (h *uiHandler) muteExpire(w http.ResponseWriter, r *http.Request) {
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
	http.Redirect(w, r, "/ui/mutes/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------
// Form helpers and rendering
// ---------------------------------------------------------------------

// newFormData is the template data for the mute create form, also
// used for re-rendering after a validation error. MatchersText is the
// raw textarea value (space-separated key=value tokens) so the user's
// edits survive a re-render.
type newFormData struct {
	Nav          string
	MatchersText string
	Comment      string
	CreatedBy    string
	StartsAt     string
	Duration     string
	EndsAt       string
	Error        map[string]string
}

func (h *uiHandler) renderNew(w http.ResponseWriter, data newFormData) {
	if data.Error == nil {
		data.Error = map[string]string{}
	}
	h.render(w, h.muteNewTmpl, data)
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

// matchersTextFromQuery builds a "key=value key=value ..." string
// from match.<name>=<value> query parameters, for use as the initial
// textarea value (the Slack→UI prefill flow). Keys are emitted in
// sorted order for deterministic output.
func matchersTextFromQuery(q url.Values) string {
	keys := make([]string, 0)
	for k := range q {
		if strings.HasPrefix(k, "match.") {
			name := strings.TrimPrefix(k, "match.")
			if name != "" && q.Get(k) != "" {
				keys = append(keys, name)
			}
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, name := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(q.Get("match." + name))
	}
	return b.String()
}

// parseMatchersText accepts whitespace-separated key=value tokens.
// The first '=' in each token is the delimiter, so values may
// contain '='. Label values containing whitespace are not supported;
// callers should enforce label-naming hygiene upstream (e.g. in the
// Prometheus alert-rule definitions that produce the labels).
func parseMatchersText(text string) (map[string]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("matchers must be non-empty")
	}
	out := map[string]string{}
	for _, tok := range strings.Fields(text) {
		i := strings.IndexByte(tok, '=')
		if i <= 0 || i == len(tok)-1 {
			return nil, fmt.Errorf("invalid matcher %q: expected key=value", tok)
		}
		out[tok[:i]] = tok[i+1:]
	}
	return out, nil
}
