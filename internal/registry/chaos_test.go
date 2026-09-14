package registry

import (
	"testing"
)

// TestChaos_ZeroValueReviewScheduler verifies that a zero-value
// ReviewScheduler does not panic on access.
func TestChaos_ZeroValueReviewScheduler(t *testing.T) {
	rs := &ReviewScheduler{}
	if rs.IsRunning() {
		t.Error("zero-value ReviewScheduler should not be running")
	}
}
