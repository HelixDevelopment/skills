package skill

import (
	"context"
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// AddDependency: pure, DB-independent guard clauses.
//
// AddDependency's self-reference check and relation-type validation both
// execute and return BEFORE any *db.Pool method is invoked (see graph.go:
// the `if skillID == dependsOn` and `if !validTypes[relType]` checks precede
// s.pool.WithTx(...)). That makes them safe to exercise against a Store with
// a nil pool -- if either guard clause were ever moved after a pool access,
// or removed, these tests would panic (nil pointer deref) or fail (wrong
// error), so they also serve as a regression trip-wire for that ordering.
// ---------------------------------------------------------------------------

func TestAddDependency_SelfReferenceIsRejectedAsCycle(t *testing.T) {
	s := NewStore(nil) // no DB required: the self-reference check returns first.
	id := uuid.New()

	err := s.AddDependency(context.Background(), id, id, models.DepTypeRequires)
	if err == nil {
		t.Fatal("expected an error for a self-referencing dependency, got nil")
	}
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("error = %v, want it to wrap ErrCycleDetected", err)
	}
}

func TestAddDependency_InvalidRelationTypeIsRejected(t *testing.T) {
	s := NewStore(nil) // no DB required: the relation-type validation returns first.
	from := uuid.New()
	to := uuid.New()

	err := s.AddDependency(context.Background(), from, to, models.DependencyType("bogus-relation"))
	if err == nil {
		t.Fatal("expected an error for an invalid relation type, got nil")
	}
	if !errors.Is(err, ErrInvalidSkill) {
		t.Errorf("error = %v, want it to wrap ErrInvalidSkill", err)
	}
}

// Note: we deliberately do NOT exercise AddDependency with a *valid*
// relation type end-to-end here. Past the two guard clauses above, the very
// next line is s.pool.WithTx(...), which immediately calls p.inner.Acquire
// on the (nil in these tests) underlying *pgxpool.Pool and panics with a nil
// pointer dereference. That is the real, DB-bound code path -- see
// TestAddDependency_DuplicateAndPersistence_RequiresLiveDatabase below.

// TestAddDependency_DuplicateAndPersistence_RequiresLiveDatabase documents
// the same boundary for the rest of AddDependency's behavior -- all of it
// runs inside s.pool.WithTx against live SQL.
//
// PARTIALLY SUPERSEDED (Fable code-review round-2, NEW-1/W2 remediation):
// dup-edge detection (the (skill_id, depends_on, relation_type)-scoped
// exists-check) and the successful-edge INSERT are now live-covered by
// TestP1T1W2_SecondTypedEdgePerPairAccepted and the TestP1T1W2_*/
// TestP1T1NEW1_* suite in graph_granularity_test.go (this package), all
// gated on the same SKILL_SYSTEM_TEST_DB_* live-database contract. What
// remains UNCOVERED by any live test: the existence-check error branches
// (ErrDependencyNotFound for a missing source or target skill ID) and
// recalcMissingDeps's registry recalculation after a successful insert --
// neither is exercised end-to-end anywhere in this package yet.
func TestAddDependency_DuplicateAndPersistence_RequiresLiveDatabase(t *testing.T) {
	t.Skip("AddDependency's existence-check ERROR branches (ErrDependencyNotFound for a " +
		"missing source/target skill) and recalcMissingDeps's registry recalculation remain " +
		"uncovered by a live test; dup-edge detection and the successful INSERT are now " +
		"covered by TestP1T1W2_SecondTypedEdgePerPairAccepted et al. in " +
		"graph_granularity_test.go. Requires an integration test against a real/containerized " +
		"Postgres instance.")
}

// ---------------------------------------------------------------------------
// collectDepNames (import_export.go): pure, in-memory dependency-name
// deduplication used when importing a TOML skill definition.
// ---------------------------------------------------------------------------

func TestCollectDepNames(t *testing.T) {
	tests := []struct {
		name string
		deps models.TOMLDependencies
		want []string
	}{
		{
			name: "empty dependencies yields empty slice",
			deps: models.TOMLDependencies{},
			want: nil,
		},
		{
			name: "single requires entry",
			deps: models.TOMLDependencies{Requires: []string{"foundations"}},
			want: []string{"foundations"},
		},
		{
			name: "requires, extends, recommends combined preserving first-seen order",
			deps: models.TOMLDependencies{
				Requires:   []string{"a", "b"},
				Extends:    []string{"c"},
				Recommends: []string{"d"},
			},
			want: []string{"a", "b", "c", "d"},
		},
		{
			name: "duplicate name across requires and extends deduplicated, first occurrence kept",
			deps: models.TOMLDependencies{
				Requires: []string{"shared", "only-requires"},
				Extends:  []string{"shared", "only-extends"},
			},
			want: []string{"shared", "only-requires", "only-extends"},
		},
		{
			name: "duplicate name within the same list deduplicated",
			deps: models.TOMLDependencies{
				Requires: []string{"dup", "dup", "unique"},
			},
			want: []string{"dup", "unique"},
		},
		{
			name: "duplicate across all three lists appears exactly once",
			deps: models.TOMLDependencies{
				Requires:   []string{"x"},
				Extends:    []string{"x"},
				Recommends: []string{"x"},
			},
			want: []string{"x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectDepNames(tt.deps, nil)
			if !equalStringSlices(got, tt.want) {
				t.Errorf("collectDepNames(%+v) = %v, want %v", tt.deps, got, tt.want)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
