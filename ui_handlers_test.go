package main

// ui_handlers_test.go drives the /ui/* HTTP handlers against a real
// PostgreSQL store, using the same harness pattern as server_test.go.
// Tests skip when DATABASE_URL is unset.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func newUIHarness(t *testing.T, cfg UIConfig) (string, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed UI test")
	}
	store, err := OpenStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	cleanTables(t, store)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := NewMetrics()
	chain := &Chain{
		Receivers: map[string]*Receiver{
			BuiltinDiscardReceiver: {Name: BuiltinDiscardReceiver, Type: "discard"},
		},
		Mutes:   store,
		History: store,
		Logger:  logger,
		Metrics: metrics,
	}
	mux := newServeMux(chain, store, metrics, logger, cfg)
	srv := httptest.NewServer(mux)

	cleanup := func() {
		srv.Close()
		store.Close()
	}
	return srv.URL, cleanup
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
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	resp, err := http.Get(base + "/ui/")
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
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(dtLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(dtLayout))
	form.Set("duration", "1h")
	form.Add("match-name", "severity")
	form.Add("match-value", "info")
	form.Set("comment", "ui test")
	form.Set("created_by", "tester")

	client := followNoRedirects()
	resp, err := client.PostForm(base+"/ui/", form)
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

	// List should now contain the new mute.
	resp2, err := http.Get(base + "/ui/")
	if err != nil {
		t.Fatalf("GET /ui/: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "ui test") {
		t.Errorf("list should contain new mute's comment 'ui test'; body excerpt:\n%s", body)
	}
	if !strings.Contains(string(body), "severity=info") {
		t.Errorf("list should show matcher; body excerpt:\n%s", body)
	}
}

func TestUICreate_EmptyCommentRerendersForm(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(dtLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(dtLayout))
	form.Add("match-name", "severity")
	form.Add("match-value", "info")
	form.Set("comment", "") // empty — should re-render
	form.Set("created_by", "tester")

	client := followNoRedirects()
	resp, err := client.PostForm(base+"/ui/", form)
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
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(dtLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(dtLayout))
	form.Add("match-name", "x")
	form.Add("match-value", "y")
	form.Set("comment", "header fallback test")
	form.Set("created_by", "") // empty in form — should fall back to header

	req, err := http.NewRequest(http.MethodPost, base+"/ui/", strings.NewReader(form.Encode()))
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
		t.Fatalf("status: got %d, want 303 (header fallback should succeed); body:\n%s", resp.StatusCode, body)
	}

	// Verify the mute records "alice" as created_by.
	resp2, _ := http.Get(base + "/ui/")
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body), "alice") {
		t.Errorf("expected created_by 'alice' in list; body:\n%s", body)
	}
}

func TestUINewForm_PrefillFromQueryParams(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	resp, err := http.Get(base + "/ui/new?match.severity=critical&match.host=web01&comment=DiskFull%20on%20web01")
	if err != nil {
		t.Fatalf("GET /ui/new: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="severity"`) {
		t.Errorf("form should prefill matcher name 'severity'; body excerpt:\n%s", excerpt(body, 2000))
	}
	if !strings.Contains(string(body), `value="critical"`) {
		t.Errorf("form should prefill matcher value 'critical'")
	}
	if !strings.Contains(string(body), "DiskFull on web01") {
		t.Errorf("form should prefill comment")
	}
}

func TestUIExpire_HTMXReturnsEmpty(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	// Create one mute via the API first.
	now := time.Now().UTC()
	form := url.Values{}
	form.Set("starts_at", now.Format(dtLayout))
	form.Set("ends_at", now.Add(time.Hour).Format(dtLayout))
	form.Add("match-name", "x")
	form.Add("match-value", "y")
	form.Set("comment", "expire test")
	form.Set("created_by", "tester")
	followNoRedirects().PostForm(base+"/ui/", form)

	// Discover the id via the API.
	resp, _ := http.Get(base + "/api/v1/mutes")
	var listed []map[string]any
	jsonDecode(t, resp, &listed)
	if len(listed) != 1 {
		t.Fatalf("expected 1 mute, got %d", len(listed))
	}
	id := listed[0]["id"].(string)

	// htmx-flavoured expire.
	req, _ := http.NewRequest(http.MethodPost, base+"/ui/"+id+"/expire", nil)
	req.Header.Set("HX-Request", "true")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("htmx POST expire: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Errorf("htmx expire should return empty body; got %q", string(body))
	}
}

func TestUIDisabled_NoRoutes(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: false})
	defer cleanup()

	resp, err := http.Get(base + "/ui/")
	if err != nil {
		t.Fatalf("GET /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("UI disabled: GET /ui/ should be 404, got %d", resp.StatusCode)
	}

	// Root path should return endpoint listing, not redirect.
	resp2, err := followNoRedirects().Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("UI disabled: GET / should be 200 text listing, got %d", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "/api/v2/alerts") {
		t.Errorf("UI disabled root should list endpoints; body:\n%s", body)
	}
}

func TestUIEnabled_RootRedirectsToUI(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	resp, err := followNoRedirects().Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("UI enabled: GET / should be 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/" {
		t.Errorf("Location: got %q, want /ui/", loc)
	}
}

func TestUIStaticAssetServed(t *testing.T) {
	base, cleanup := newUIHarness(t, UIConfig{Enabled: true, UserHeader: "X-Auth-User"})
	defer cleanup()

	resp, err := http.Get(base + "/ui/static/htmx.min.js")
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

// excerpt returns the first n bytes of b for diagnostic logging.
func excerpt(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

// jsonDecode reads resp.Body into v. Helper for short JSON bodies.
func jsonDecode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(body), err)
	}
}
