// verify.go implements `alertchain verify`: a table of routing
// expectations checked against the static configuration.
//
// Scope: routing only. Mutes are runtime state and out of scope here;
// use `alertchain trace` for mute questions.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// verifyFile is the top-level YAML structure.
type verifyFile struct {
	Verify []verifyCase `yaml:"verify"`
}

// verifyCase is one row in the routing verification table.
//
// Expect.Rules and Expect.Receivers are both required and must match
// exactly (order-independent). Omitting either would leave that
// dimension free to drift unnoticed.
type verifyCase struct {
	Name        string     `yaml:"name"`
	Labels      labelMap   `yaml:"labels"`
	Expect      expectSpec `yaml:"expect"`
	Description string     `yaml:"description,omitempty"`
}

// labelMap is the alert's label set.
type labelMap map[string]string

// expectSpec declares what rules and receivers must be reached.
type expectSpec struct {
	Rules     []string `yaml:"rules"`
	Receivers []string `yaml:"receivers"`
}

// LoadVerifyCases reads a YAML file of verify cases and validates the
// per-case schema (names non-empty). Schema problems are reported
// with the file path and the offending case name so that the operator
// can find the source line.
func LoadVerifyCases(path string) ([]verifyCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f verifyFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, c := range f.Verify {
		if c.Name == "" {
			return nil, fmt.Errorf("%s: case #%d: name is required", path, i+1)
		}
	}
	return f.Verify, nil
}

// Verify runs every case in cases against chain and writes a report to
// w. It returns true when all cases pass, false when any failed.
//
// The function is pure with respect to the chain: it only calls
// Chain.Evaluate, which has no I/O. The notification history and
// mute store are not consulted.
func Verify(chain *Chain, cases []verifyCase, w io.Writer) (allPass bool) {
	allPass = true
	pass, fail := 0, 0

	for _, c := range cases {
		alert := &Alert{Labels: map[string]string(c.Labels)}
		decisions := chain.Evaluate(alert)

		gotRules := make([]string, 0, len(decisions))
		gotReceivers := make([]string, 0, len(decisions))
		for _, d := range decisions {
			gotRules = append(gotRules, d.Rule.Name)
			gotReceivers = append(gotReceivers, d.Rule.Receiver)
		}

		mismatches := compareExpectations(c.Expect, gotRules, gotReceivers)

		if len(mismatches) == 0 {
			fmt.Fprintf(w, "PASS  %s\n", c.Name)
			pass++
			continue
		}

		allPass = false
		fail++
		fmt.Fprintf(w, "FAIL  %s\n", c.Name)
		if c.Description != "" {
			fmt.Fprintf(w, "      %s\n", c.Description)
		}
		for _, m := range mismatches {
			fmt.Fprintf(w, "      %s\n", m)
		}
	}

	fmt.Fprintf(w, "\n%d passed, %d failed, %d total\n", pass, fail, len(cases))
	return allPass
}

// compareExpectations returns a slice of human-readable mismatch
// descriptions, or an empty slice when everything matches. Comparison
// is order-independent for both rules and receivers (a fan-out via
// continue:true may reach receivers in any deterministic order, but
// the case author should not depend on it).
//
// Returning a slice rather than a single error lets the report show
// all mismatches at once: an operator debugging a case can see both
// "wrong rules" and "wrong receivers" in the same run, rather than
// fixing one and discovering the other on the next iteration.
func compareExpectations(expect expectSpec, gotRules, gotReceivers []string) []string {
	var msgs []string
	if !sameSet(expect.Rules, gotRules) {
		msgs = append(msgs,
			fmt.Sprintf("expected rules     %v, got %v", sortedCopy(expect.Rules), sortedCopy(gotRules)))
	}
	if !sameSet(expect.Receivers, gotReceivers) {
		msgs = append(msgs,
			fmt.Sprintf("expected receivers %v, got %v", sortedCopy(expect.Receivers), sortedCopy(gotReceivers)))
	}
	return msgs
}

// sameSet reports whether a and b contain the same multiset of
// strings. Order is ignored; duplicates count.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := sortedCopy(a)
	cb := sortedCopy(b)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
