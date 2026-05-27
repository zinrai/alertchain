// api_mutes.go implements the mute endpoints of the HTTP API.
//
// The wire format is alertchain's own, intentionally not Alertmanager
// v2 silence compatible. See DESIGN.md for the reasoning: the silence
// API surface is shaped by Alertmanager's repeating-notification model
// (auto-silence scripts, ChatOps integrations, schedulers) which has
// no place in alertchain.
//
// Routes:
//
//	GET    /api/v1/mutes         list all mutes
//	POST   /api/v1/mutes         create
//	GET    /api/v1/mutes/{id}    get one
//	DELETE /api/v1/mutes/{id}    expire immediately
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// muteIn is the JSON shape accepted by POST /api/v1/mutes. Matchers
// is a label-name to expected-value map; all entries must match the
// alert's labels (logical AND).
type muteIn struct {
	Matchers  map[string]string `json:"matchers"`
	StartsAt  time.Time         `json:"starts_at"`
	EndsAt    time.Time         `json:"ends_at"`
	Comment   string            `json:"comment,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
}

// muteOut is the JSON shape returned by GET endpoints. Status is
// computed server-side from the current time.
type muteOut struct {
	ID        string            `json:"id"`
	Matchers  map[string]string `json:"matchers"`
	StartsAt  time.Time         `json:"starts_at"`
	EndsAt    time.Time         `json:"ends_at"`
	Comment   string            `json:"comment,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	Status    string            `json:"status"` // "pending" | "active" | "expired"
}

type mutesHandler struct {
	store  MuteStore
	logger *slog.Logger
}

func newMutesHandler(store MuteStore, logger *slog.Logger) *mutesHandler {
	return &mutesHandler{store: store, logger: logger}
}

func (h *mutesHandler) listCreate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.list(w, r)
	case http.MethodPost:
		h.create(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *mutesHandler) getOrExpire(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/mutes/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "invalid mute id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.get(w, r, id)
	case http.MethodDelete:
		h.expire(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *mutesHandler) list(w http.ResponseWriter, r *http.Request) {
	mutes, err := h.store.List(r.Context())
	if err != nil {
		http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	out := make([]muteOut, 0, len(mutes))
	for _, m := range mutes {
		out = append(out, muteToOut(m, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *mutesHandler) create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var in muteIn
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(in.Matchers) == 0 {
		http.Error(w, "matchers must be non-empty", http.StatusBadRequest)
		return
	}
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() {
		http.Error(w, "starts_at and ends_at are required", http.StatusBadRequest)
		return
	}
	if !in.EndsAt.After(in.StartsAt) {
		http.Error(w, "ends_at must be after starts_at", http.StatusBadRequest)
		return
	}

	m := &Mute{
		ID:        NewMuteID(),
		Matchers:  in.Matchers,
		StartsAt:  in.StartsAt.UTC(),
		EndsAt:    in.EndsAt.UTC(),
		Comment:   in.Comment,
		CreatedBy: in.CreatedBy,
	}
	if err := h.store.Create(r.Context(), m); err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": m.ID})
}

func (h *mutesHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	m, err := h.store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, muteToOut(m, time.Now().UTC()))
}

func (h *mutesHandler) expire(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.store.Expire(r.Context(), id); err != nil {
		http.Error(w, "expire: "+err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// muteToOut renders a Mute as its API output form, computing the
// pending/active/expired status from `now`. The boundary semantics
// match the internal Mute.Active method: a mute is active on the
// closed interval [StartsAt, EndsAt].
func muteToOut(m *Mute, now time.Time) muteOut {
	var status string
	switch {
	case now.Before(m.StartsAt):
		status = "pending"
	case now.After(m.EndsAt):
		status = "expired"
	default:
		status = "active"
	}
	return muteOut{
		ID:        m.ID,
		Matchers:  m.Matchers,
		StartsAt:  m.StartsAt,
		EndsAt:    m.EndsAt,
		Comment:   m.Comment,
		CreatedBy: m.CreatedBy,
		Status:    status,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
