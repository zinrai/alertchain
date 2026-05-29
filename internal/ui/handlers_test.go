package ui

// handlers_test.go drives the /ui/* HTTP handlers against a real
// PostgreSQL store, using the same harness pattern as the api package's
// server_test.go. Tests skip when DATABASE_URL is unset.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zinrai/alertchain/internal/alertchain"
	"github.com/zinrai/alertchain/internal/store"
)

type uiHarness struct {
	base    string
	db      *store.Store
	cleanup func()
}

func newUIHarness(t *testing.T, cfg alertchain.UIConfig) *uiHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed UI test")
	}
	db, err := store.OpenStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := db.TruncateForTesting(context.Background()); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	if cfg.Enabled {
		Mount(mux, db, logger, cfg)
	}
	srv := httptest.NewServer(mux)

	return &uiHarness{
		base: srv.URL,
		db:   db,
		cleanup: func() {
			srv.Close()
			db.Close()
		},
	}
}

// followNoRedirects returns an http.Client that does not follow
// redirects, so tests can assert on 303 Location values.
func followNoRedirects() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestUIList_Empty(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	resp, err := http.Get(h.base + "/ui/")
	if err != nil {
		t.Fatalf("GET /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "No mutes yet") {
		t.Errorf("empty list page should mention 'No mutes yet'; body:\n%s", body)
	}
}

func TestUICreate_ValidPayload(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(DTLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(DTLayout))
	form.Set("duration", "1h")
	form.Add("match-name", "severity")
	form.Add("match-value", "info")
	form.Set("comment", "ui test")
	form.Set("created_by", "tester")

	resp, err := followNoRedirects().PostForm(h.base+"/ui/", form)
	if err != nil {
		t.Fatalf("POST /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 303; body:\n%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/" {
		t.Errorf("redirect Location: got %q, want %q", loc, "/ui/")
	}

	// Verify the mute was persisted by reading directly through the
	// lifecycle function (this is what the API and UI both go through).
	views, err := alertchain.ListMutes(context.Background(), h.db)
	if err != nil {
		t.Fatalf("ListMutes: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 mute persisted, got %d", len(views))
	}
	if views[0].Comment != "ui test" || views[0].CreatedBy != "tester" {
		t.Errorf("persisted mute fields wrong: %+v", views[0])
	}
}

func TestUICreate_EmptyCommentRerendersForm(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(DTLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(DTLayout))
	form.Add("match-name", "severity")
	form.Add("match-value", "info")
	form.Set("comment", "")
	form.Set("created_by", "tester")

	resp, err := followNoRedirects().PostForm(h.base+"/ui/", form)
	if err != nil {
		t.Fatalf("POST /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (form re-render)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "comment is required") {
		t.Errorf("expected validation error 'comment is required'; body:\n%s", body)
	}
}

func TestUICreate_HeaderFallbackForCreatedBy(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(DTLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(DTLayout))
	form.Add("match-name", "x")
	form.Add("match-value", "y")
	form.Set("comment", "header fallback test")
	form.Set("created_by", "")

	req, err := http.NewRequest(http.MethodPost, h.base+"/ui/", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Auth-User", "alice")

	resp, err := followNoRedirects().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 303; body:\n%s", resp.StatusCode, body)
	}

	views, _ := alertchain.ListMutes(context.Background(), h.db)
	if len(views) != 1 || views[0].CreatedBy != "alice" {
		t.Errorf("expected created_by=alice, got: %+v", views)
	}
}

func TestUINewForm_PrefillFromQueryParams(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	resp, err := http.Get(h.base + "/ui/new?match.severity=critical&match.host=web01&comment=DiskFull%20on%20web01")
	if err != nil {
		t.Fatalf("GET /ui/new: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="severity"`) {
		t.Errorf("form should prefill matcher name 'severity'")
	}
	if !strings.Contains(string(body), `value="critical"`) {
		t.Errorf("form should prefill matcher value 'critical'")
	}
	if !strings.Contains(string(body), "DiskFull on web01") {
		t.Errorf("form should prefill comment")
	}
}

func TestUIExpire_HTMXReturnsEmpty(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	now := time.Now().UTC()
	view, err := alertchain.CreateMute(context.Background(), h.db, alertchain.CreateMuteRequest{
		Matchers:  map[string]string{"x": "y"},
		StartsAt:  now,
		EndsAt:    now.Add(time.Hour),
		Comment:   "expire test",
		CreatedBy: "tester",
	})
	if err != nil {
		t.Fatalf("CreateMute: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, h.base+"/ui/"+view.ID+"/expire", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("htmx POST expire: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("htmx expire should return empty body; got %q", string(body))
	}
}

// TestUIEnabled_RootRedirectsToUI verifies that ui.Mount registers
// the "/" → "/ui/" redirect when called.
func TestUIEnabled_RootRedirectsToUI(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	resp, err := followNoRedirects().Get(h.base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("GET / should be 302 when UI enabled, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/" {
		t.Errorf("Location: got %q, want /ui/", loc)
	}
}

// TestUIDisabled_NoRoutes verifies that when ui.Mount is not called,
// the /ui/ routes are not registered (404).
func TestUIDisabled_NoRoutes(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: false})
	defer h.cleanup()

	resp, err := http.Get(h.base + "/ui/")
	if err != nil {
		t.Fatalf("GET /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("UI disabled: GET /ui/ should be 404, got %d", resp.StatusCode)
	}
}

func TestUIStaticAssetServed(t *testing.T) {
	h := newUIHarness(t, alertchain.UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer h.cleanup()

	resp, err := http.Get(h.base + "/ui/static/htmx.min.js")
	if err != nil {
		t.Fatalf("GET htmx.min.js: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("htmx.min.js: got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if !strings.Contains(string(body), "htmx") {
		t.Errorf("htmx.min.js content does not contain 'htmx'")
	}
}
