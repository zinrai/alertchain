package alertchain

import (
	"testing"
)

// TestMetricsNilReceiverSafe verifies that all increment methods are
// safe to call on a nil *Metrics. This is the contract Chain relies on
// when running unit tests that leave Metrics unset.
func TestMetricsNilReceiverSafe(t *testing.T) {
	var m *Metrics
	// These must not panic.
	m.IncAlertsReceived()
	m.IncNotifySuccess()
	m.IncNotifyFailure()
	m.IncMuteLookupFailure()
	m.IncHistoryLookupFailure()
	m.IncHistoryWriteFailure()
}
