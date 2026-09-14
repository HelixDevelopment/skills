package skill

// HXC-159 T-P3.01.5 (A6): the cross-skill edge-write for the `part_of`
// child→parent alias — part_of=[P] on an imported child must materialize as
// a parent→child `composes` edge in the same transaction.
//
// §11.4.115 RED-first: TestPartOf_InvertsToParentComposesEdge FAILS on the
// pre-fix code (ImportFromTOML hard-errors via ErrPartOfUnsupported) and
// PASSES once the inversion is wired. Live-DB contract identical to the G07
// suite (g07NewLiveStore → skillSkipIfNoTestDB; honest t.Skip without PG).

import (
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
)

func TestPartOf_InvertsToParentComposesEdge(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "po2.parent")

	body := `
[skill]
name = "po2.child"
version = "0.1.0"
title = "part_of child"
content = "content"

[skill.dependencies]
part_of = ["po2.parent"]
`
	child, err := store.ImportFromTOML(ctx, []byte(body))
	if err != nil {
		t.Fatalf("import with part_of failed: %v (want the inverted parent→child composes edge)", err)
	}
	if child == nil || child.Name != "po2.child" {
		t.Fatalf("imported skill = %+v, want po2.child", child)
	}

	// The child itself carries no outgoing edges for the alias.
	if len(child.Dependencies) != 0 {
		t.Errorf("child outgoing edges = %d, want 0 (part_of materializes on the parent)", len(child.Dependencies))
	}

	// The parent must now carry parent→child `composes`.
	parent, err := store.GetByName(ctx, "po2.parent")
	if err != nil {
		t.Fatalf("GetByName(po2.parent): %v", err)
	}
	found := false
	for _, d := range parent.Dependencies {
		if d.RelationType == models.DepTypeComposes && d.DependsOn == child.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no parent→child composes edge on po2.parent (deps=%+v)", parent.Dependencies)
	}

	// Round-trip: the parent's export must list the child under composes, and
	// re-importing that export into a fresh DB must reproduce the edge.
	exp, err := store.ExportToTOML(ctx, "po2.parent")
	if err != nil {
		t.Fatalf("ExportToTOML(po2.parent): %v", err)
	}
	if !contains(string(exp), "po2.child") {
		t.Errorf("parent export does not mention po2.child:\n%s", exp)
	}
}

func TestPartOf_AbsentParent_HardErrors_NoPartialPersist(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	body := `
[skill]
name = "po2.orphan"
version = "0.1.0"
title = "orphan child"
content = "content"

[skill.dependencies]
part_of = ["po2.ghost"]
`
	_, err := store.ImportFromTOML(ctx, []byte(body))
	if err == nil {
		t.Fatalf("import with part_of to an absent parent succeeded; want a hard error (no silent partial import)")
	}
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("import error = %v, want it to wrap ErrDependencyNotFound", err)
	}
	if _, gerr := store.GetByName(ctx, "po2.orphan"); gerr == nil {
		t.Errorf("po2.orphan was created despite the hard error (no-partial-persist violated)")
	}
}

func TestPartOf_DuplicateParents_FoldToOneEdge(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "po2.foldparent")

	body := `
[skill]
name = "po2.foldchild"
version = "0.1.0"
title = "fold child"
content = "content"

[skill.dependencies]
part_of = ["po2.foldparent", "po2.foldparent"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(body)); err != nil {
		t.Fatalf("import with duplicated part_of failed: %v", err)
	}
	parent, err := store.GetByName(ctx, "po2.foldparent")
	if err != nil {
		t.Fatalf("GetByName(po2.foldparent): %v", err)
	}
	n := 0
	for _, d := range parent.Dependencies {
		if d.RelationType == models.DepTypeComposes {
			n++
		}
	}
	if n != 1 {
		t.Errorf("parent composes edges = %d, want 1 (duplicate part_of must fold)", n)
	}
}

func TestPartOf_CycleThroughParent_HardErrors(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	// Chain: cyc.base (leaf) <- cyc.mid (requires base) <- cyc.top (requires mid).
	g07ImportLeaf(t, ctx, store, "cyc.base")
	midTOML := `
[skill]
name = "cyc.mid"
version = "0.1.0"
title = "mid"
content = "content"

[skill.dependencies]
requires = ["cyc.base"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(midTOML)); err != nil {
		t.Fatalf("import cyc.mid: %v", err)
	}
	topTOML := `
[skill]
name = "cyc.top"
version = "0.1.0"
title = "top"
content = "content"

[skill.dependencies]
requires = ["cyc.mid"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(topTOML)); err != nil {
		t.Fatalf("import cyc.top: %v", err)
	}

	// Inverting cyc.base→cyc.top would close base→top→mid→base: must refuse.
	body := `
[skill]
name = "cyc.top2"
version = "0.1.0"
title = "top2"
content = "content"

[skill.dependencies]
requires = ["cyc.top"]
part_of = ["cyc.base"]
`
	_, err := store.ImportFromTOML(ctx, []byte(body))
	if err == nil {
		t.Fatalf("cyclic part_of inversion succeeded; want ErrCycleDetected")
	}
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("import error = %v, want it to wrap ErrCycleDetected", err)
	}
	if _, gerr := store.GetByName(ctx, "cyc.top2"); gerr == nil {
		t.Errorf("cyc.top2 was created despite the cycle error (no-partial-persist violated)")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
