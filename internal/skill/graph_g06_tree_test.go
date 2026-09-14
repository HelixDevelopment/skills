package skill

// G06 (register GAPS_AND_RISKS_REGISTER.md §G06): GetDependencyTree assembled
// the dependency tree by attaching ONLY the root's direct children
// (graph.go: `root.Children = childrenMap[rootSkill.ID]`), so grandchildren and
// deeper transitive dependencies were silently dropped -- the recursive
// "endless skill branching" tree was truncated to depth-1. These live-DB tests
// (a) reproduce the truncation: a transitive grandchild/great-grandchild is
// MISSING pre-fix -> RED, GREEN once the assembly recurses to every node; and
// (b) prove the recursive assembly is CYCLE-GUARDED: an advisory cycle
// A->B->C->A (permitted by the granularity model -- recommends/related_to/
// alternative_to are exempt from hard-closure acyclicity, see AddDependency)
// terminates and yields a finite emit-once tree, never an infinite recursion /
// stack overflow. The `seen`-set guard is load-bearing: removing it turns the
// cycle case into unbounded recursion (§1.1 mutation).
//
// Contract: same live SKILL_SYSTEM_TEST_DB_* database + throwaway-DB +
// real-migrations harness as migration_granularity_test.go /
// kind_read_paths_granularity_test.go in this package (§11.4.27 no mocks).

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
)

// collectTreeDescendants walks a SkillTreeNode recursively (children of
// children, ...) and returns every DESCENDANT skill name (excluding the root),
// duplicates included so callers can assert emit-once. A generous depth cap is
// a safety net only: a correctly cycle-guarded tree is finite and never reaches
// it, but the cap prevents a regressed (unguarded) tree from hanging the
// harness during collection.
func collectTreeDescendants(node *models.SkillTreeNode) []string {
	var out []string
	var walk func(n *models.SkillTreeNode, depth int)
	walk = func(n *models.SkillTreeNode, depth int) {
		if depth > 1000 {
			return
		}
		for i := range n.Children {
			out = append(out, n.Children[i].Skill.Name)
			walk(&n.Children[i], depth+1)
		}
	}
	walk(node, 0)
	return out
}

func childNames(nodes []models.SkillTreeNode) []string {
	out := make([]string, len(nodes))
	for i := range nodes {
		out[i] = nodes[i].Skill.Name
	}
	return out
}

func g06CreateSkill(t *testing.T, ctx context.Context, store *Store, name string) *models.Skill {
	t.Helper()
	sk := &models.Skill{
		Name:    name,
		Title:   name,
		Content: name + " content",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create skill %q: %v", name, err)
	}
	return sk
}

func g06NewLiveStore(t *testing.T, ctx context.Context) (*Store, func()) {
	t.Helper()
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return nil, nil
	}
	dbCfg, cleanup := skillCreateThrowawayDB(t, admin)
	pool, err := db.New(dbCfg)
	if err != nil {
		cleanup()
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		pool.Close()
		cleanup()
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}
	return NewStore(pool), func() {
		pool.Close()
		cleanup()
	}
}

// TestG06_GetDependencyTree_TransitiveDepthLive reproduces + guards the depth-1
// truncation: a 4-level chain A->B->C->D must come back fully nested. Pre-fix,
// B.Children is empty (only root's direct children were attached), so the
// grandchild C and great-grandchild D are missing -> this test FAILS (RED).
func TestG06_GetDependencyTree_TransitiveDepthLive(t *testing.T) {
	ctx := context.Background()
	store, teardown := g06NewLiveStore(t, ctx)
	if store == nil {
		return
	}
	defer teardown()

	a := g06CreateSkill(t, ctx, store, "g06.a.root")
	b := g06CreateSkill(t, ctx, store, "g06.b.child")
	c := g06CreateSkill(t, ctx, store, "g06.c.grandchild")
	d := g06CreateSkill(t, ctx, store, "g06.d.greatgrandchild")

	for _, e := range []struct{ from, to uuid.UUID }{
		{a.ID, b.ID}, {b.ID, c.ID}, {c.ID, d.ID},
	} {
		if err := store.AddDependency(ctx, e.from, e.to, models.DepTypeRequires); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", e.from, e.to, err)
		}
	}

	tree, err := store.GetDependencyTree(ctx, "g06.a.root", 10)
	if err != nil {
		t.Fatalf("GetDependencyTree: %v", err)
	}

	if len(tree.Children) != 1 || tree.Children[0].Skill.Name != "g06.b.child" {
		t.Fatalf("root children = %v, want single child g06.b.child", childNames(tree.Children))
	}
	bNode := &tree.Children[0]
	// The load-bearing G06 assertion: the transitive grandchild must be linked
	// under B. Pre-fix this is empty (depth-1 truncation).
	if len(bNode.Children) != 1 || bNode.Children[0].Skill.Name != "g06.c.grandchild" {
		t.Fatalf("transitive dependency truncated: B.Children = %v, want [g06.c.grandchild] "+
			"(G06 depth-1 assembly bug -- grandchildren never linked)", childNames(bNode.Children))
	}
	cNode := &bNode.Children[0]
	if len(cNode.Children) != 1 || cNode.Children[0].Skill.Name != "g06.d.greatgrandchild" {
		t.Fatalf("deep transitive dependency truncated: C.Children = %v, want [g06.d.greatgrandchild]",
			childNames(cNode.Children))
	}

	// Full closure present, each transitive dep exactly once (emit-once).
	counts := map[string]int{}
	got := collectTreeDescendants(tree)
	for _, n := range got {
		counts[n]++
	}
	for _, n := range []string{"g06.b.child", "g06.c.grandchild", "g06.d.greatgrandchild"} {
		if counts[n] != 1 {
			t.Errorf("descendant %q appears %d time(s), want exactly 1", n, counts[n])
		}
	}
	if len(got) != 3 {
		t.Errorf("tree has %d descendants %v, want 3", len(got), got)
	}
}

// TestG06_GetDependencyTree_AdvisoryCycleTerminatesLive proves the assembly is
// cycle-guarded. A->B->C are hard `requires` edges; C->A is an advisory
// `recommends` edge that closes a cycle (advisory edges are cycle-exempt, so
// the granularity model allows this). A correct, cycle-guarded traversal
// terminates and returns a finite emit-once tree; an unguarded recursive
// assembly would loop forever on the closing edge.
func TestG06_GetDependencyTree_AdvisoryCycleTerminatesLive(t *testing.T) {
	baseCtx := context.Background()
	store, teardown := g06NewLiveStore(t, baseCtx)
	if store == nil {
		return
	}
	defer teardown()

	a := g06CreateSkill(t, baseCtx, store, "g06cyc.a")
	b := g06CreateSkill(t, baseCtx, store, "g06cyc.b")
	c := g06CreateSkill(t, baseCtx, store, "g06cyc.c")
	if err := store.AddDependency(baseCtx, a.ID, b.ID, models.DepTypeRequires); err != nil {
		t.Fatalf("AddDependency A->B (requires): %v", err)
	}
	if err := store.AddDependency(baseCtx, b.ID, c.ID, models.DepTypeRequires); err != nil {
		t.Fatalf("AddDependency B->C (requires): %v", err)
	}
	// Advisory back-edge closing the cycle; must be accepted (cycle-exempt).
	if err := store.AddDependency(baseCtx, c.ID, a.ID, models.DepTypeRecommends); err != nil {
		t.Fatalf("AddDependency C->A (advisory recommends, closes cycle): %v", err)
	}

	ctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
	defer cancel()

	done := make(chan struct{})
	var tree *models.SkillTreeNode
	var gerr error
	go func() {
		tree, gerr = store.GetDependencyTree(ctx, "g06cyc.a", 10)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("GetDependencyTree did not terminate on an advisory cycle within 20s (cycle guard missing)")
	}
	if gerr != nil {
		t.Fatalf("GetDependencyTree: %v", gerr)
	}

	// Finite + emit-once: closure = {B, C}. The C->A edge closes the cycle and
	// must be dropped, never re-attach the root as its own descendant.
	counts := map[string]int{}
	got := collectTreeDescendants(tree)
	for _, n := range got {
		counts[n]++
	}
	if counts["g06cyc.a"] != 0 {
		t.Errorf("root re-attached as its own descendant %d time(s) -- cycle not guarded", counts["g06cyc.a"])
	}
	if counts["g06cyc.b"] != 1 || counts["g06cyc.c"] != 1 {
		t.Errorf("descendant counts b=%d c=%d, want each exactly 1 (emit-once)", counts["g06cyc.b"], counts["g06cyc.c"])
	}
	if len(got) != 2 {
		t.Errorf("cycle tree has %d descendants %v, want 2 {b,c}", len(got), got)
	}
}
