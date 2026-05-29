// config.go parses the alertchain YAML file into a *Chain.
package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type configFile struct {
	Receivers []configReceiver `yaml:"receivers"`
	Rules     []configRule     `yaml:"rules"`
	UI        *configUI        `yaml:"ui,omitempty"`
}

type configReceiver struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`

	URL     string `yaml:"url,omitempty"`
	URLFile string `yaml:"url_file,omitempty"`
}

type configRule struct {
	Name     string            `yaml:"name"`
	Match    map[string]string `yaml:"match"`
	Receiver string            `yaml:"receiver"`
	Continue bool              `yaml:"continue,omitempty"`
}

// configUI uses pointers so absent fields can be distinguished from
// explicit zero values (default kicks in for the absent case only).
type configUI struct {
	Enabled    *bool   `yaml:"enabled,omitempty"`
	UserHeader *string `yaml:"user_header,omitempty"`
}

// UIConfig is the resolved UI configuration (post-defaults). Consumed
// by newServeMux and the UI handlers.
type UIConfig struct {
	Enabled    bool   // default true
	UserHeader string // default "X-Auth-User"; empty means no header lookup
}

// Config bundles everything LoadConfig produces.
type Config struct {
	Chain *Chain
	UI    UIConfig
}

// LoadConfig reads a YAML file and returns a fully populated *Config.
// External dependencies on the chain (mute store, history, notifier,
// logger, metrics) are left nil; the caller is expected to set them
// before calling Process.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cf configFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	chain := &Chain{
		Receivers: map[string]*Receiver{},
	}

	for i, cr := range cf.Receivers {
		if cr.Name == "" {
			return nil, fmt.Errorf("receiver #%d: name is required", i+1)
		}
		if cr.Name == BuiltinDiscardReceiver {
			return nil, fmt.Errorf("receiver %q: name is reserved for the built-in discard receiver; remove this declaration", cr.Name)
		}
		if cr.Type == "discard" {
			return nil, fmt.Errorf("receiver %q: type %q is reserved; the discard receiver is built-in and cannot be declared", cr.Name, cr.Type)
		}
		if _, exists := chain.Receivers[cr.Name]; exists {
			return nil, fmt.Errorf("receiver %q: duplicate name", cr.Name)
		}
		r := &Receiver{
			Name:    cr.Name,
			Type:    cr.Type,
			URL:     cr.URL,
			URLFile: cr.URLFile,
		}
		if err := r.resolveFileFields(); err != nil {
			return nil, fmt.Errorf("receiver %q: %w", cr.Name, err)
		}
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("receiver %q: %w", cr.Name, err)
		}
		chain.Receivers[cr.Name] = r
	}

	// Built-in receiver: a rule may target "discard" to drop matching
	// alerts.
	chain.Receivers[BuiltinDiscardReceiver] = &Receiver{
		Name: BuiltinDiscardReceiver,
		Type: "discard",
	}

	for _, cr := range cf.Rules {
		rule := &Rule{
			Name:     cr.Name,
			Match:    cr.Match,
			Receiver: cr.Receiver,
			Continue: cr.Continue,
		}
		chain.Rules = append(chain.Rules, rule)
	}

	if err := chain.Validate(); err != nil {
		return nil, err
	}

	ui := UIConfig{
		Enabled:    true,
		UserHeader: "X-Auth-User",
	}
	if cf.UI != nil {
		if cf.UI.Enabled != nil {
			ui.Enabled = *cf.UI.Enabled
		}
		if cf.UI.UserHeader != nil {
			ui.UserHeader = *cf.UI.UserHeader
		}
	}

	return &Config{Chain: chain, UI: ui}, nil
}

// resolveFileFields reads *_file fields and populates the corresponding
// inline fields.
func (r *Receiver) resolveFileFields() error {
	if r.URLFile != "" && r.URL == "" {
		b, err := os.ReadFile(r.URLFile)
		if err != nil {
			return fmt.Errorf("read url_file %s: %w", r.URLFile, err)
		}
		r.URL = strings.TrimSpace(string(b))
	}
	return nil
}

// validate checks the type-specific required fields. The "discard"
// type is intentionally not handled here: users cannot declare it (it
// is rejected at LoadConfig), so reaching this function with that type
// is impossible.
func (r *Receiver) validate() error {
	switch r.Type {
	case "":
		return fmt.Errorf("type is required")
	case "webhook":
		if r.URL == "" {
			return fmt.Errorf("webhook receiver requires url or url_file")
		}
	default:
		return fmt.Errorf("unknown receiver type %q (only \"webhook\" is configurable)", r.Type)
	}
	return nil
}
