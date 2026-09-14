package skillsource

import (
	"testing"
)

// TestAuditEventConstants_NonEmpty verifies that every exported audit event
// constant is a non-empty string. This is a compile-time-adjacent guard: if a
// constant is accidentally left empty, this test catches it before any audit
// log consumer sees a blank event type.
func TestAuditEventConstants_NonEmpty(t *testing.T) {
	events := map[string]string{
		"EventSourceRegistered": EventSourceRegistered,
		"EventSourceUpdated":    EventSourceUpdated,
		"EventSourceDeleted":    EventSourceDeleted,
		"EventSourceSyncStart":  EventSourceSyncStart,
		"EventSourceSyncEnd":    EventSourceSyncEnd,
		"EventSourceSyncFailed": EventSourceSyncFailed,
		"EventSkillImported":    EventSkillImported,
		"EventSkillSkipped":     EventSkillSkipped,
	}
	for name, val := range events {
		if val == "" {
			t.Errorf("audit event constant %s is empty", name)
		}
	}
}

// TestAuditEventConstants_Unique verifies that all audit event constants have
// distinct values, preventing accidental collisions in audit log queries.
func TestAuditEventConstants_Unique(t *testing.T) {
	events := []string{
		EventSourceRegistered,
		EventSourceUpdated,
		EventSourceDeleted,
		EventSourceSyncStart,
		EventSourceSyncEnd,
		EventSourceSyncFailed,
		EventSkillImported,
		EventSkillSkipped,
	}
	seen := make(map[string]string, len(events))
	for _, val := range events {
		if prev, exists := seen[val]; exists {
			t.Errorf("duplicate audit event value %q (also used by %s)", val, prev)
		}
		seen[val] = val
	}
}

// TestAuditEventConstants_PrefixConvention verifies that all event constants
// follow the "source." prefix convention documented in events.go.
func TestAuditEventConstants_PrefixConvention(t *testing.T) {
	events := map[string]string{
		"EventSourceRegistered": EventSourceRegistered,
		"EventSourceUpdated":    EventSourceUpdated,
		"EventSourceDeleted":    EventSourceDeleted,
		"EventSourceSyncStart":  EventSourceSyncStart,
		"EventSourceSyncEnd":    EventSourceSyncEnd,
		"EventSourceSyncFailed": EventSourceSyncFailed,
		"EventSkillImported":    EventSkillImported,
		"EventSkillSkipped":     EventSkillSkipped,
	}
	for name, val := range events {
		if len(val) < 7 || val[:7] != "source." {
			t.Errorf("audit event constant %s = %q does not start with 'source.'", name, val)
		}
	}
}
