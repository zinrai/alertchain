// verify.go implements `alertchain verify`.
//
// Verify runs a table of routing expectations against the loaded
// configuration and reports whether each case routes to the expected
// rules and receivers. The intended use is pre-deployment
// verification in CI: a PR that changes alertchain.yaml is
// accompanied by a change to the verify file, and a regression in
// routing behavior fails the CI job.
//
// Scope: the verify command checks the static routing table only.
// Mutes are runtime state managed via the HTTP API and are not part
// of the deployable configuration, so they are out of scope here.
// Use `alertchain trace` against a running server for ad-hoc
// "would this mute suppress this alert" questions.
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
// Labels are the alert's labels (same shape as the JSON body of POST
// /api/v2/alerts, minus the wrapping fields). Expect.Rules and
// Expect.Receivers are both required: both names must match exactly,
// order-independent. A case that asserts the rule path but not the
// receiver, or vice versa, would leave the other dimension free to
// drift unnoticed, so the schema does not allow it.
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
// strings. Order is ignored. Duplicates count: this matches the
// alertchain semantics where each Decision is a distinct (rule,
// alert) pair, so two decisions reaching the same receiver are
// counted twice.
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
