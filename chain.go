// chain.go defines the core types and the rule evaluation logic.
//
// The evaluation model is iptables-like: rules are evaluated top-to-bottom,
// matching rules produce decisions, and the chain stops at the first match
// unless the rule has Continue=true.
//
// There is no tree, no inheritance, no parent fallback, no per-rule
// defaults block. A rule's behavior is fully self-contained.
//
// Matching is label-equality only. A rule's Match is a map from label
// name to expected value, and all entries must match exactly (logical
// AND) for the rule to apply. Conditions that would require negation,
// alternation, or regex are expressed by writing multiple rules in the
// order that produces the desired routing — see DESIGN.md for the
// rationale.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/common/model"
)

// BuiltinDiscardReceiver is the name of the built-in receiver that
// drops alerts without notifying anywhere. It is always present in
// Chain.Receivers; users cannot declare a receiver with this name.
const BuiltinDiscardReceiver = "discard"

// notifyTimeout bounds the total time a single webhook delivery is
// allowed to take. The Process loop applies this via context.WithTimeout
// on every Notify invocation. Keeping the timeout in one place avoids
// the dual-timeout pitfalls of also setting it on the http.Client.
const notifyTimeout = 10 * time.Second

// matchAll reports whether every (name, want) entry in conditions
// equals the corresponding entry in labels. An empty conditions map
// matches anything (catch-all).
func matchAll(conditions, labels map[string]string) bool {
	for name, want := range conditions {
		if labels[name] != want {
			return false
		}
	}
	return true
}

// Rule is one row in the chain. Rules are evaluated top-to-bottom in
// config-file order. A rule is completely self-contained; there is no
// inheritance from anywhere.
type Rule struct {
	Name     string            // required, unique, used as state key
	Match    map[string]string // empty or nil means catch-all
	Receiver string            // required, must reference a configured receiver
	Continue bool              // when true, continue evaluating subsequent rules
}

// Receiver describes how to deliver a notification. Only the "webhook"
// type is configurable; the "discard" type is built-in and is injected
// by LoadConfig (users cannot declare a receiver of that type or with
// that name).
//
// For destinations other than a generic webhook (Slack, Email,
// PagerDuty, etc.), point a webhook receiver at a small adapter service
// that translates the payload. This responsibility is intentionally
// kept outside alertchain.
type Receiver struct {
	Name    string
	Type    string // "webhook" | "discard"
	URL     string // webhook only
	URLFile string // webhook only (URL is read from this file at load time)
}

// Alert is the unit of input. Field names follow the Alertmanager v2 API
// for compatibility.
type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt,omitempty"`
	EndsAt       time.Time         `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

// Fingerprint computes a stable identifier for the alert from its labels.
// The implementation delegates to model.LabelSet.Fingerprint() from
// prometheus/common/model, so the resulting value is byte-identical to
// the one Alertmanager produces for the same label set. A webhook
// receiver that deduplicates Alertmanager-sourced alerts by fingerprint
// will deduplicate alertchain-sourced alerts identically.
func (a *Alert) Fingerprint() string {
	ls := make(model.LabelSet, len(a.Labels))
	for k, v := range a.Labels {
		ls[model.LabelName(k)] = model.LabelValue(v)
	}
	return ls.Fingerprint().String()
}

// Decision is the result of matching one rule against one alert.
type Decision struct {
	Rule  *Rule
	Alert *Alert
}

// Chain is the top-level evaluation engine. It holds the rules, the
// receivers map, the mute store, and the notification history store.
type Chain struct {
	Rules     []*Rule
	Receivers map[string]*Receiver
	Mutes     MuteStore
	History   NotificationHistory
	Notifier  Notifier
	Logger    *slog.Logger
	Metrics   *Metrics // optional; nil disables counter updates
}

// Evaluate runs the alert against the rule list and returns all matching
// decisions. This is a pure function over (rules, alert); no I/O.
func (c *Chain) Evaluate(alert *Alert) []Decision {
	var decisions []Decision
	for _, r := range c.Rules {
		if !matchAll(r.Match, alert.Labels) {
			continue
		}
		decisions = append(decisions, Decision{Rule: r, Alert: alert})
		if !r.Continue {
			break
		}
	}
	return decisions
}

// Process is the main entry point during `serve`. It checks mutes,
// evaluates the chain, applies the firing/resolved state machine, and
// dispatches notifications.
//
// Error contract: Process returns a non-nil error if and only if the
// alert could not be fully evaluated due to a database failure (mute
// store lookup or history store lookup). Webhook delivery failures and
// history-write failures are recorded and/or logged but never surface
// as an error from Process. The rationale: when a DB read fails the
// alert has not yet been acted on and re-pushing it from the sender
// side is the correct recovery; when a webhook side-effect has already
// occurred, aborting cannot undo it, and the next push will be
// deduplicated by the webhook receiver via fingerprint.
func (c *Chain) Process(ctx context.Context, alert *Alert) error {
	muted, err := c.Mutes.Matches(ctx, alert)
	if err != nil {
		c.Metrics.incMuteLookupFailure()
		return fmt.Errorf("mute check: %w", err)
	}
	if muted {
		c.Logger.Debug("alert muted", "fingerprint", alert.Fingerprint())
		return nil
	}

	decisions := c.Evaluate(alert)
	if len(decisions) == 0 {
		c.Logger.Debug("alert matched no rule, dropping", "fingerprint", alert.Fingerprint())
		return nil
	}

	now := time.Now().UTC()
	fp := alert.Fingerprint()

	for _, d := range decisions {
		recv := c.Receivers[d.Rule.Receiver]
		if recv == nil {
			c.Logger.Warn("rule references unknown receiver",
				"rule", d.Rule.Name, "receiver", d.Rule.Receiver)
			continue
		}
		if recv.Type == "discard" {
			continue
		}

		desired := desiredStatus(alert, now)

		prevStatus, ok, err := c.History.LastAttempt(ctx, d.Rule.Name, fp)
		if err != nil {
			c.Metrics.incHistoryLookupFailure()
			return fmt.Errorf("history lookup (rule=%s): %w", d.Rule.Name, err)
		}
		if ok && prevStatus == desired {
			c.Logger.Debug("already delivered, skipping",
				"rule", d.Rule.Name, "fingerprint", fp, "status", string(desired))
			continue
		}

		notifyCtx, cancel := context.WithTimeout(ctx, notifyTimeout)
		notifyErr := c.Notifier.Notify(notifyCtx, recv, alert)
		cancel()

		status := desired
		if notifyErr != nil {
			c.Logger.Error("notification failed",
				"rule", d.Rule.Name, "receiver", recv.Name, "err", notifyErr)
			c.Metrics.incNotifyFailure()
			status = desired.Failed()
		} else {
			c.Metrics.incNotifySuccess()
		}

		// RecordAttempt failure is intentionally not propagated: the
		// webhook side-effect (if any) has already occurred and cannot
		// be undone by aborting. The next push from the sender side
		// will produce another delivery, which webhook receivers
		// deduplicate by fingerprint.
		if err := c.History.RecordAttempt(ctx, d.Rule.Name, fp, now, status); err != nil {
			c.Logger.Error("history write failed",
				"rule", d.Rule.Name, "err", err)
			c.Metrics.incHistoryWriteFailure()
		}
	}
	return nil
}

// NotificationStatus represents the recorded outcome of the most
// recent notification attempt for a given (rule, fingerprint) pair.
type NotificationStatus string

const (
	StatusFiringSent     NotificationStatus = "firing-sent"
	StatusFiringFailed   NotificationStatus = "firing-failed"
	StatusResolvedSent   NotificationStatus = "resolved-sent"
	StatusResolvedFailed NotificationStatus = "resolved-failed"
)

// Failed returns the failure-state counterpart of a sent status. It is
// used to record the desired status as failed when the webhook attempt
// returns an error.
func (s NotificationStatus) Failed() NotificationStatus {
	switch s {
	case StatusFiringSent:
		return StatusFiringFailed
	case StatusResolvedSent:
		return StatusResolvedFailed
	}
	return s
}

// desiredStatus returns the status that should be recorded if the
// upcoming notification attempt succeeds. The two "sent" values are
// the only ones returned here; "failed" variants are derived inside
// Process when the attempt errors.
//
// The boundary policy is the closed interval: an alert whose EndsAt
// equals now is considered resolved. This matches Mute.Active, which
// also uses [StartsAt, EndsAt] closed-interval semantics, so the
// "equal to now" case is treated consistently across the codebase.
func desiredStatus(a *Alert, now time.Time) NotificationStatus {
	if !a.EndsAt.IsZero() && !a.EndsAt.After(now) {
		return StatusResolvedSent
	}
	return StatusFiringSent
}

// Validate checks invariants on the loaded chain. Called by `check` and
// at startup.
func (c *Chain) Validate() error {
	seenNames := map[string]bool{}
	for i, r := range c.Rules {
		if r.Name == "" {
			return fmt.Errorf("rule #%d: name is required", i+1)
		}
		if seenNames[r.Name] {
			return fmt.Errorf("rule #%d: duplicate name %q", i+1, r.Name)
		}
		seenNames[r.Name] = true

		if r.Receiver == "" {
			return fmt.Errorf("rule %q: receiver is required", r.Name)
		}
		if c.Receivers[r.Receiver] == nil {
			return fmt.Errorf("rule %q: references undefined receiver %q",
				r.Name, r.Receiver)
		}
	}
	return nil
}
