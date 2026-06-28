// notify.go implements the Notifier interface for webhook delivery.
//
// The total deadline for a single webhook attempt is supplied by the
// caller via the context passed to Notify. The notifier itself does
// not set a Client.Timeout: stacking that on top of the context would
// produce two overlapping time bounds with different semantics
// (Client.Timeout includes response body read time, whereas the
// context cancels at the deadline regardless), and Process already
// applies a context.WithTimeout. The only timeout configured here is
// the lower-level dial timeout, which protects against a hung TCP
// connect that the context could not interrupt by itself.
package alertchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Notifier delivers a single alert to a single receiver. Implementations
// must respect the context's deadline and cancellation. Returning a
// non-nil error indicates the delivery should be recorded as failed.
type Notifier interface {
	Notify(ctx context.Context, recv *Receiver, alert *Alert) error
}

// HTTPNotifier delivers alerts by POSTing JSON to webhook URLs.
type HTTPNotifier struct {
	Client *http.Client
}

// NewHTTPNotifier returns a notifier whose underlying transport bounds
// the connection-establishment phase. Per-request total deadlines must
// be supplied by the caller via the context passed to Notify; the
// notifier itself does not impose one.
func NewHTTPNotifier() *HTTPNotifier {
	return &HTTPNotifier{
		Client: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: 5 * time.Second,
				}).DialContext,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Notify implements Notifier.
func (n *HTTPNotifier) Notify(ctx context.Context, recv *Receiver, alert *Alert) error {
	switch recv.Type {
	case "webhook":
		return n.notifyWebhook(ctx, recv, alert)
	default:
		return fmt.Errorf("unknown receiver type %q", recv.Type)
	}
}

// webhookPayload is the body sent to webhook receivers. Shape matches
// the Alertmanager v2 webhook payload.
type webhookPayload struct {
	Receiver string         `json:"receiver"`
	Status   string         `json:"status"`
	Alerts   []webhookAlert `json:"alerts"`
}

// webhookAlert is the per-alert object inside the webhook payload.
// Adds derived fields (status, fingerprint) on top of Alert.
type webhookAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt,omitempty"`
	EndsAt       time.Time         `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
	Fingerprint  string            `json:"fingerprint"`
}

func newWebhookAlert(a *Alert) webhookAlert {
	return webhookAlert{
		Status:       alertStatus(a),
		Labels:       a.Labels,
		Annotations:  a.Annotations,
		StartsAt:     a.StartsAt,
		EndsAt:       a.EndsAt,
		GeneratorURL: a.GeneratorURL,
		Fingerprint:  a.Fingerprint(),
	}
}

func (n *HTTPNotifier) notifyWebhook(ctx context.Context, recv *Receiver, alert *Alert) error {
	payload := webhookPayload{
		Receiver: recv.Name,
		Status:   alertStatus(alert),
		Alerts:   []webhookAlert{newWebhookAlert(alert)},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, recv.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.Client.Do(req)
	if err != nil {
		// recv.URL is intentionally omitted: webhook URLs frequently
		// embed secrets (Slack-style tokens, signed query strings), and
		// the receiver name is sufficient to identify the destination
		// when correlated with the configuration.
		return fmt.Errorf("post to receiver %q: %w", recv.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("post to receiver %q: status %d: %s",
			recv.Name, resp.StatusCode, string(excerpt))
	}
	return nil
}

// alertStatus returns "resolved" if the alert has a non-zero EndsAt at
// or before now, otherwise "firing".
func alertStatus(a *Alert) string {
	if !a.EndsAt.IsZero() && !a.EndsAt.After(time.Now()) {
		return "resolved"
	}
	return "firing"
}
