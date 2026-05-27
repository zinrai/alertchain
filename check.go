// check.go implements `alertchain check`.
//
// Check validates the configuration file and reports warnings for
// patterns that are valid but typically indicate a misconfiguration:
// - the last rule is not a catch-all
// - a rule references the built-in "discard" receiver as a catch-all
//
// Errors cause a non-zero exit. Warnings are advisory.
package main

import (
	"fmt"
	"io"
)

// Check validates the chain. Returns nil on success, an error on
// invalid configuration. Warnings are written to w.
func Check(c *Chain, w io.Writer) error {
	if len(c.Rules) == 0 {
		fmt.Fprintln(w, "warning: no rules defined; all alerts will be dropped")
		return nil
	}

	last := c.Rules[len(c.Rules)-1]
	if len(last.Match) != 0 {
		fmt.Fprintln(w, "warning: last rule is not a catch-all; alerts not matching any rule will be silently dropped")
	}
	if len(last.Match) == 0 {
		recv := c.Receivers[last.Receiver]
		if recv != nil && recv.Type == "discard" {
			fmt.Fprintln(w, "note: last rule is catch-all -> discard; unmatched alerts will be dropped explicitly")
		}
	}

	// Warn about always-match rules in non-final position, which would
	// catch alerts before later rules can see them.
	for i, r := range c.Rules[:len(c.Rules)-1] {
		if len(r.Match) == 0 && !r.Continue {
			fmt.Fprintf(w, "warning: rule #%d (%q) is a non-continue catch-all in non-final position; subsequent rules will never execute\n",
				i+1, r.Name)
		}
	}
	return nil
}
