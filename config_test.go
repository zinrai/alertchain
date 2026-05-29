package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempYAML writes content to a temp file and returns its path.
// The file is registered for cleanup at test end.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "alertchain.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}
	return path
}

func TestLoadConfigBasic(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: webhook
    type: webhook
    url: http://example.com/x

rules:
  - name: critical
    match:
      severity: critical
      team: infra
    receiver: webhook
    continue: true
  - name: catch
    match: {}
    receiver: discard
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	chain := cfg.Chain
	if len(chain.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(chain.Rules))
	}

	r0 := chain.Rules[0]
	if r0.Name != "critical" {
		t.Errorf("rule 0 name: got %q", r0.Name)
	}
	if r0.Match["severity"] != "critical" || r0.Match["team"] != "infra" {
		t.Errorf("rule 0 match: %v", r0.Match)
	}
	if !r0.Continue {
		t.Errorf("rule 0 continue should be true")
	}

	r1 := chain.Rules[1]
	if len(r1.Match) != 0 {
		t.Errorf("rule 1 should be catch-all, got %v", r1.Match)
	}
	if r1.Receiver != "discard" {
		t.Errorf("rule 1 receiver: got %q", r1.Receiver)
	}
}

func TestLoadConfigCatchAllWithoutMatchKey(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: catch
    receiver: discard
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	chain := cfg.Chain
	if len(chain.Rules[0].Match) != 0 {
		t.Errorf("omitting match should be equivalent to catch-all, got %v", chain.Rules[0].Match)
	}
}

func TestLoadConfigRejectsReservedReceiverName(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: discard
    type: webhook
    url: http://x

rules:
  - name: r
    receiver: discard
`)
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("expected error when receiver name is the reserved 'discard'")
	}
}

func TestLoadConfigRejectsReservedReceiverType(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: my-discard
    type: discard

rules:
  - name: r
    receiver: my-discard
`)
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("expected error when user declares type=discard")
	}
}

func TestLoadConfigRequiresReceiverURL(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook

rules:
  - name: r
    receiver: w
`)
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("expected error when webhook receiver has no url")
	}
}

func TestLoadConfigRejectsUnknownReceiverInRule(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: r
    receiver: nonexistent
`)
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("expected error when rule references undefined receiver")
	}
}

func TestLoadConfigBuiltinDiscardAlwaysAvailable(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: drop-noisy
    match:
      source: noisy
    receiver: discard
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	chain := cfg.Chain
	r := chain.Receivers["discard"]
	if r == nil || r.Type != "discard" {
		t.Errorf("built-in discard receiver should be present, got %+v", r)
	}
}

func TestLoadConfigDuplicateRuleName(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: same
    receiver: w
  - name: same
    receiver: w
`)
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("expected error for duplicate rule names")
	}
}

func TestLoadConfigURLFile(t *testing.T) {
	dir := t.TempDir()
	urlFile := filepath.Join(dir, "url")
	if err := os.WriteFile(urlFile, []byte("http://example.com/from-file\n"), 0o600); err != nil {
		t.Fatalf("write url file: %v", err)
	}
	cfgPath := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
receivers:
  - name: w
    type: webhook
    url_file: `+urlFile+`

rules:
  - name: r
    receiver: w
`), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Chain.Receivers["w"].URL; got != "http://example.com/from-file" {
		t.Errorf("url from file: got %q", got)
	}
}

func TestLoadConfigUIDefaults(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: catch
    receiver: discard
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.UI.Enabled {
		t.Errorf("UI.Enabled default: got false, want true")
	}
	if cfg.UI.UserHeader != "X-Auth-User" {
		t.Errorf("UI.UserHeader default: got %q, want %q", cfg.UI.UserHeader, "X-Auth-User")
	}
}

func TestLoadConfigUIDisabled(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: catch
    receiver: discard

ui:
  enabled: false
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.UI.Enabled {
		t.Errorf("UI.Enabled: got true, want false")
	}
}

func TestLoadConfigUICustomUserHeader(t *testing.T) {
	path := writeTempYAML(t, `
receivers:
  - name: w
    type: webhook
    url: http://x

rules:
  - name: catch
    receiver: discard

ui:
  user_header: X-Forwarded-User
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.UI.UserHeader != "X-Forwarded-User" {
		t.Errorf("UI.UserHeader: got %q, want %q", cfg.UI.UserHeader, "X-Forwarded-User")
	}
}
