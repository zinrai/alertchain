package main

import (
	"testing"
)

// TestCompareExpectationsRejectsMismatch verifies that mismatches in
// either rules or receivers produce a failure description. This
// underpins the validity of every verify result: if
// compareExpectations returned empty for a non-matching case, all
// PASS verdicts in CI would be unreliable.
func TestCompareExpectationsRejectsMismatch(t *testing.T) {
	tests := []struct {
		name     string
		expect   expectSpec
		gotRules []string
		gotRecvs []string
		wantPass bool
	}{
		{
			name:     "exact match passes",
			expect:   expectSpec{Rules: []string{"r1", "r2"}, Receivers: []string{"a", "b"}},
			gotRules: []string{"r1", "r2"},
			gotRecvs: []string{"a", "b"},
			wantPass: true,
		},
		{
			name:     "order-independent match passes",
			expect:   expectSpec{Rules: []string{"r1", "r2"}, Receivers: []string{"a", "b"}},
			gotRules: []string{"r2", "r1"},
			gotRecvs: []string{"b", "a"},
			wantPass: true,
		},
		{
			name:     "missing rule fails",
			expect:   expectSpec{Rules: []string{"r1", "r2"}, Receivers: []string{"a"}},
			gotRules: []string{"r1"},
			gotRecvs: []string{"a"},
			wantPass: false,
		},
		{
			name:     "extra rule fails",
			expect:   expectSpec{Rules: []string{"r1"}, Receivers: []string{"a"}},
			gotRules: []string{"r1", "r2"},
			gotRecvs: []string{"a"},
			wantPass: false,
		},
		{
			name:     "wrong receiver fails even when rules match",
			expect:   expectSpec{Rules: []string{"r1"}, Receivers: []string{"a"}},
			gotRules: []string{"r1"},
			gotRecvs: []string{"b"},
			wantPass: false,
		},
		{
			name:     "duplicate receivers are counted",
			expect:   expectSpec{Rules: []string{"r1", "r2"}, Receivers: []string{"a", "a"}},
			gotRules: []string{"r1", "r2"},
			gotRecvs: []string{"a", "a"},
			wantPass: true,
		},
		{
			name:     "single vs duplicate receiver fails",
			expect:   expectSpec{Rules: []string{"r1"}, Receivers: []string{"a"}},
			gotRules: []string{"r1", "r2"},
			gotRecvs: []string{"a", "a"},
			wantPass: false,
		},
		{
			name:     "empty expect and empty got pass (no rule matched)",
			expect:   expectSpec{Rules: []string{}, Receivers: []string{}},
			gotRules: []string{},
			gotRecvs: []string{},
			wantPass: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := compareExpectations(tc.expect, tc.gotRules, tc.gotRecvs)
			gotPass := len(msgs) == 0
			if gotPass != tc.wantPass {
				t.Errorf("got pass=%v (msgs=%v), want pass=%v", gotPass, msgs, tc.wantPass)
			}
		})
	}
}
