// trace.go implements `alertchain trace`: a one-shot, side-effect-free
// dry run of the chain against a hypothetical alert.
package alertchain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Trace runs alert through the chain and writes a human-readable trace.
// If muteStore is non-nil, mute matching is included in the trace.
func Trace(ctx context.Context, c *Chain, muteStore MuteStore,
	alert *Alert, w io.Writer) error {

	fmt.Fprintf(w, "Alert:\n")
	fmt.Fprintf(w, "  fingerprint: %s\n", alert.Fingerprint())
	fmt.Fprintf(w, "  labels:      %s\n", labelsToString(alert.Labels))
	fmt.Fprintln(w)

	if muteStore != nil {
		muted, err := muteStore.Matches(ctx, alert)
		if err != nil {
			return fmt.Errorf("mute check: %w", err)
		}
		if muted {
			fmt.Fprintln(w, "Mute check: MATCHED (alert would be muted)")
			return nil
		}
		fmt.Fprintln(w, "Mute check: not matched")
	} else {
		fmt.Fprintln(w, "Mute check: skipped (no store provided)")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Rule evaluation:")
	matched := false
	var finals []*Rule
	for i, r := range c.Rules {
		idx := fmt.Sprintf("[%d]", i+1)
		cond := labelsToString(r.Match)
		if cond == "" {
			cond = "(catch-all)"
		}
		if !MatchAll(r.Match, alert.Labels) {
			fmt.Fprintf(w, "  %s %-30s skip   %s\n", idx, r.Name, cond)
			continue
		}
		matched = true
		action := "stop"
		if r.Continue {
			action = "continue"
		}
		fmt.Fprintf(w, "  %s %-30s MATCH  -> %s  (%s)  %s\n",
			idx, r.Name, r.Receiver, action, cond)
		finals = append(finals, r)
		if !r.Continue {
			break
		}
	}
	if !matched {
		fmt.Fprintln(w, "  (no rule matched)")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Final decisions:")
	if len(finals) == 0 {
		fmt.Fprintln(w, "  (none, alert would be dropped)")
		return nil
	}
	for _, r := range finals {
		recv := c.Receivers[r.Receiver]
		typ := "?"
		if recv != nil {
			typ = recv.Type
		}
		fmt.Fprintf(w, "  -> notify  (rule: %s, type: %s)\n", r.Name, typ)
	}
	return nil
}

// LoadAlertFromFile reads a JSON file and parses it as an Alert.
func LoadAlertFromFile(path string) (*Alert, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a Alert
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	return &a, nil
}

func labelsToString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%q", k, labels[k])
	}
	return out
}
