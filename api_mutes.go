// api_mutes.go implements the /api/v1/mutes endpoints.
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
	"errors"
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
	views, err := ListMutes(r.Context(), h.store)
	if err != nil {
		http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if views == nil {
		views = []MuteView{}
	}
	writeJSON(w, http.StatusOK, views)
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
	view, err := CreateMute(r.Context(), h.store, CreateMuteRequest{
		Matchers:  in.Matchers,
		StartsAt:  in.StartsAt,
		EndsAt:    in.EndsAt,
		Comment:   in.Comment,
		CreatedBy: in.CreatedBy,
	})
	if err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			http.Error(w, ve.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": view.ID})
}

func (h *mutesHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	view, err := GetMute(r.Context(), h.store, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *mutesHandler) expire(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.store.Expire(r.Context(), id); err != nil {
		http.Error(w, "expire: "+err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
