package skill

// G33 — ExportToTOML must never emit an empty dependency edge name (live-DB).
//
// Register: GAPS_AND_RISKS_REGISTER.md G33 ("Store.ExportToTOML swallowed a
// row-scan error -> empty dep name in exported TOML").
//
// Forensic (§11.4.6 evidence): the pre-fix ExportToTOML resolved an
// unpopulated dependency name with `_ = s.pool.QueryRow(...).Scan(&name)` — the
// scan error was swallowed, and whenever that fallback produced an empty
// `name`, a blank dependency edge (`requires = [""]`) was written into the
// exported TOML. The fallback is reachable when a dependency's target skill has
// an empty name: skills.name is `NOT NULL UNIQUE` with NO non-empty CHECK
// (migrations/001_initial.up.sql), and Store.Create performs no name
// validation, so a blank-named target is insertable and its edge exports blank.
// This reproduces the corruption deterministically through the real store
// (§11.4.27 — no mocks).
//
// §11.4.115 RED-first / polarity: on the pre-fix code this test FAILS —
// ExportToTOML returns (bytes, nil) whose bytes carry a blank `requires`
// entry. The GREEN post-fix behaviour asserted here is that the export FAILS
// loudly (naming the unresolvable target) rather than emit an empty edge. The
// paired §1.1 mutation = reverting the G33 guard restores this RED failure.
//
// Gated on the SKILL_SYSTEM_TEST_DB_* contract (g07NewLiveStore ->
// skillSkipIfNoTestDB); absent a configured PostgreSQL it honestly t.Skip()s
// (§11.4.3/§11.4.27).

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
)

func TestG33_ExportRefusesEmptyDependencyName(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	// Target skill with an EMPTY name — the exact condition that drives
	// ExportToTOML's dependency-name fallback to yield an empty name. A blank
	// name is insertable (skills.name is NOT NULL UNIQUE with no non-empty
	// CHECK; Store.Create does not validate it).
	blank := &models.Skill{
		Name:    "",
		Version: "0.1.0",
		Title:   "blank-named leaf",
		Content: "leaf content",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, blank); err != nil {
		t.Fatalf("create blank-named target skill: %v", err)
	}

	// Source skill that requires the blank-named target. The edge is added by
	// ID via the store's own API, since name-based import skips empty names.
	src := &models.Skill{
		Name:    "g33.src",
		Version: "0.2.0",
		Title:   "g33 export source",
		Content: "source content",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindComposite,
	}
	if err := store.Create(ctx, src); err != nil {
		t.Fatalf("create source skill: %v", err)
	}
	if err := store.AddDependency(ctx, src.ID, blank.ID, models.DepTypeRequires); err != nil {
		t.Fatalf("add requires edge to blank-named target: %v", err)
	}

	// GREEN (post-fix): the export refuses to emit an empty dependency edge.
	// RED (pre-fix): ExportToTOML returns (bytes, nil) whose bytes carry a
	// blank `requires = [""]` entry.
	out, err := store.ExportToTOML(ctx, src.Name)
	if err == nil {
		t.Fatalf("ExportToTOML returned no error and emitted a dependency edge with an empty name (G33 corruption); TOML:\n%s", out)
	}
	if !strings.Contains(err.Error(), blank.ID.String()) {
		t.Errorf("export error should name the unresolvable dependency target %s; got: %v", blank.ID, err)
	}
}
