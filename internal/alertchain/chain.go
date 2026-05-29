// chain.go defines the core types and the rule evaluation loop.
package alertchain

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

// notifyTimeout bounds a single webhook delivery. Applied via
// context.WithTimeout in Process; do not also set it on http.Client
// (the two timeouts have different cancellation semantics).
const notifyTimeout = 10 * time.Second

// MatchAll reports whether every (name, want) entry in conditions
// equals the corresponding entry in labels. An empty conditions map
// matches anything (catch-all).
func MatchAll(conditions, labels map[string]string) bool {
	for name, want := range conditions {
		if labels[name] != want {
			return false
		}
	}
	return true
}

// Rule is one row in the chain. Rules are evaluated top-to-bottom in
// config-file order.
type Rule struct {
	Name     string            // required, unique, used as state key
	Match    map[string]string // empty or nil means catch-all
	Receiver string            // required, must reference a configured receiver
	Continue bool              // when true, continue evaluating subsequent rules
}

// Receiver describes how to deliver a notification. The "discard" type
// is built-in and injected by LoadConfig; users cannot declare a
// receiver of that type or with that name.
type Receiver struct {
	Name    string
	Type    string // "webhook" | "discard"
	URL     string // webhook only
	URLFile string // webhook only (URL is read from this file at load time)
}

// Alert is the unit of input. Field names mirror the Alertmanager v2
// PostableAlert shape.
type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt,omitempty"`
	EndsAt       time.Time         `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

// Fingerprint computes a stable identifier for the alert from its
// labels. The value is byte-identical to model.LabelSet.Fingerprint()
// from prometheus/common/model.
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
		if !MatchAll(r.Match, alert.Labels) {
			continue
		}
		decisions = append(decisions, Decision{Rule: r, Alert: alert})
		if !r.Continue {
			break
		}
	}
	return decisions
}

// Process evaluates one alert through mutes + rules and dispatches any
// matching notifications.
//
// Error contract: Process returns a non-nil error if and only if a
// database read failed (mute store lookup or history store lookup).
// Webhook delivery failures and history-write failures are recorded
// and/or logged but never surface as an error from Process.
func (c *Chain) Process(ctx context.Context, alert *Alert) error {
	muted, err := c.Mutes.Matches(ctx, alert)
	if err != nil {
		c.Metrics.IncMuteLookupFailure()
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
			c.Metrics.IncHistoryLookupFailure()
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
			c.Metrics.IncNotifyFailure()
			status = desired.Failed()
		} else {
			c.Metrics.IncNotifySuccess()
		}

		// RecordAttempt failure does not propagate: the webhook
		// side-effect has already occurred and aborting cannot
		// undo it.
		if err := c.History.RecordAttempt(ctx, d.Rule.Name, fp, now, status); err != nil {
			c.Logger.Error("history write failed",
				"rule", d.Rule.Name, "err", err)
			c.Metrics.IncHistoryWriteFailure()
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

// Failed returns the failure-state counterpart of a sent status.
func (s NotificationStatus) Failed() NotificationStatus {
	switch s {
	case StatusFiringSent:
		return StatusFiringFailed
	case StatusResolvedSent:
		return StatusResolvedFailed
	}
	return s
}

// desiredStatus returns the status to record if the upcoming
// notification attempt succeeds. Only the two "sent" values are
// returned; "failed" variants are derived inside Process.
//
// Boundary: an alert with non-zero EndsAt <= now is considered
// resolved (closed interval).
func desiredStatus(a *Alert, now time.Time) NotificationStatus {
	if !a.EndsAt.IsZero() && !a.EndsAt.After(now) {
		return StatusResolvedSent
	}
	return StatusFiringSent
}

// Validate checks invariants on the loaded chain.
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
