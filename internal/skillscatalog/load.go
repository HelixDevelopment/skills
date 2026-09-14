package skillscatalog

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
)

// ErrDefensiveCheck is the sentinel wrapped by every generator-side integrity
// check that fails closed on an input the schema SHOULD make impossible but
// this generator does not trust blindly (DESIGN.md §6 golden-bad fixtures).
// Callers should compare with errors.Is.
var ErrDefensiveCheck = errors.New("skillscatalog: defensive integrity check failed")

// defaultMaxRosterRows is passed to Store.ListSkills, via Config.MaxRosterRows
// (see below), in place of an "unbounded" rows count. ListSkills has no such
// sentinel itself (its SQL is a literal `LIMIT $N`, and Postgres' `LIMIT 0`
// returns ZERO rows, not "no limit", internal/skill/store.go:719) --
// §11.4.6: rather than guess a different "unbounded" semantics into an
// existing, unmodified API, this generator passes a limit large enough to
// exceed any real corpus size this project's own scale notes anticipate
// (CODEBASE_MAP.md §2: 8 skills today; DESIGN.md §2.6: an
// order-of-magnitude growth-trigger discussion at 150).
//
// PROMOTED out of a package-private VAR into this const + the
// Config.MaxRosterRows field loadRoster now reads (F-E review finding,
// round 3, 2026-07-16). The former var was mutated in place by
// TestSkillsCatalog_GoldenBad_ListAllLimitReached (save the original value,
// shrink it, defer its restore) to exercise the `len(base) == limit`
// refusal branch below without seeding a genuinely million-row fixture --
// safe ONLY as long as no test in this package ever ran with t.Parallel(),
// since two goroutines racing a shared package-level var through `go test
// -race` is exactly the data race that flag exists to catch. Threading the
// limit through Config removes the shared-mutable-package-state hazard
// entirely, rather than merely documenting it: every caller (production
// AND test) now passes its own value on its own Config, so no test needs
// to touch, save, or restore anything package-global, and a future
// t.Parallel() test gains nothing to race.
const defaultMaxRosterRows = 1_000_000

// loadRoster reads every skill in store, together with its dependencies
// (grouped+sorted into the six canonical relation types), its reverse
// dependents, and its resources, via the EXISTING Store read methods
// (Store.ListSkills, Store.GetByName, Store.GetDependents, Store.Pool --
// CODEBASE_MAP.md §4.4) -- no new Store method, no modification to any
// existing file.
//
// Store.ListSkills/Store.GetByName do not populate Dependencies/Resources in
// bulk across the whole skill set in one call (CODEBASE_MAP.md §4.4), so
// this is an N+1 read: one ListSkills call, then one GetByName + one
// GetDependents per skill. At the project's current/anticipated scale
// (CODEBASE_MAP.md §2, DESIGN.md §2.6) this is the documented, accepted
// scale trade-off flagged (not silently assumed away) by DESIGN.md §3's own
// "Recommended location" note -- a bulk join query is future work if the
// corpus grows large enough to matter.
//
// Defensive checks (DESIGN.md §6 golden-bad fixtures): a skill with an empty
// Name, or a dependency edge whose target no longer resolves to a live
// skill row (a dangling edge a concurrent delete could in principle produce
// even though the schema's ON DELETE CASCADE FK makes it unreachable
// through normal Store-level mutation), both hard-error here rather than
// silently degrade into a broken/degenerate generated file.
//
// cfg.MaxRosterRows bounds the single Store.ListSkills call this function
// makes (F-E review finding, round 3, 2026-07-16 -- see
// defaultMaxRosterRows' doc comment above); a zero-or-negative value (the
// zero Config's own zero value included -- Config's doc comment already
// says the zero Config is NOT the intended default) falls back to
// defaultMaxRosterRows rather than passing a nonsensical limit to
// Store.ListSkills.
func loadRoster(ctx context.Context, store *skill.Store, cfg Config) ([]skillRecord, error) {
	maxRows := cfg.MaxRosterRows
	if maxRows <= 0 {
		maxRows = defaultMaxRosterRows
	}
	base, err := store.ListSkills(ctx, "", maxRows, 0)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	if len(base) == maxRows {
		return nil, fmt.Errorf("%w: ListSkills returned exactly the configured row limit (%d) rows -- "+
			"the real roster may be larger and silently truncated by that LIMIT; refusing to generate a "+
			"possibly-incomplete catalog rather than silently omit skills past the limit", ErrDefensiveCheck, maxRows)
	}

	records := make([]skillRecord, 0, len(base))
	for _, sk := range base {
		if sk.Name == "" {
			return nil, fmt.Errorf("%w: a skill row (id=%s) has an empty name -- refusing to generate a degenerate catalog page for it", ErrDefensiveCheck, sk.ID)
		}

		full, err := store.GetByName(ctx, sk.Name)
		if err != nil {
			return nil, fmt.Errorf("load full skill %q: %w", sk.Name, err)
		}

		if err := verifyNoDanglingEdges(ctx, store, *full); err != nil {
			return nil, err
		}

		md, err := decodeMetadata(full.Metadata)
		if err != nil {
			return nil, fmt.Errorf("decode metadata for skill %q: %w", full.Name, err)
		}

		rec := skillRecord{
			Skill:      *full,
			Metadata:   md,
			NameSlug:   slugify(full.Name),
			DepsByType: groupAndSortDeps(full.Dependencies),
			Resources:  full.Resources,
		}
		if md.Domain != "" {
			rec.DomainSlug = slugify(md.Domain)
		}

		dependents, err := store.GetDependents(ctx, full.ID)
		if err != nil {
			return nil, fmt.Errorf("get dependents for skill %q: %w", full.Name, err)
		}
		rec.Dependents = dedupeDependentsByID(dependents)

		records = append(records, rec)
	}

	if err := checkNoSlugCollisions(records); err != nil {
		return nil, err
	}

	// base is already ORDER BY name (store.go:719); this re-sort is a
	// defence-in-depth restatement of the SAME invariant on the fully-loaded
	// record set, never a substitute for it.
	sort.Slice(records, func(i, j int) bool { return records[i].Skill.Name < records[j].Skill.Name })
	return records, nil
}

// reservedUnclassifiedDomainSlug is the EXACT filesystem slug writeByDomain
// (generate.go) reserves for the "_unclassified.md" bucket page it writes
// for the pseudo-group of every skill whose Metadata.Domain == "" (model.go's
// DomainSlug doc comment: DomainSlug is "" iff Domain == ""). Verified
// against generate.go's own writeByDomain: `filepath.Join(dir,
// "_unclassified.md")` is the literal path written whenever len(unclassified)
// > 0 -- the slug component of that path, with the ".md" extension stripped
// to match how every OTHER by-domain slug is compared in this file, is
// exactly "_unclassified".
//
// checkNoSlugCollisions (below) already refuses two DISTINCT real
// (non-empty) domains that slugify to the SAME value; before this fix it did
// NOT ALSO refuse a real, non-empty domain that slugifies to THIS reserved
// value. Left unchecked, a roster containing BOTH (a) a skill whose
// Metadata.Domain is non-empty and slugifies (model.go's slugify) to
// "_unclassified" -- e.g. "_Unclassified", "_unclassified", ".unclassified",
// or "&unclassified", every one of which slugify identically -- AND (b) a
// skill whose Metadata.Domain == "" (a genuinely unclassified skill) causes
// writeByDomain to write BOTH `by-domain/_unclassified.md` (for the real
// domain's members, keyed off its slug) AND `by-domain/_unclassified.md`
// (for the unclassified bucket) to the IDENTICAL path -- whichever write
// runs last silently wins, and the OTHER group's skill(s) disappear from the
// generated tree on every subsequent run with no error raised (Finding 1,
// round 5, 2026-07-16; the F2-review-finding slug-collision class this same
// function already guards, applied to the ONE slug value that is reserved
// by CONSTRUCTION rather than by coincidence with another skill's domain).
//
// The reservation is UNCONDITIONAL -- it fires even when no skill in THIS
// roster currently has an empty Domain -- because a roster is not static: a
// later skill import that introduces the FIRST unclassified skill must not
// silently start clobbering an already-generated by-domain page for a
// domain that happens to slugify to "_unclassified"; refusing the collision
// the moment the conflicting domain slug itself appears, regardless of
// whether the bucket is populated yet, is the simplest rule that is safe
// under every future roster mutation.
const reservedUnclassifiedDomainSlug = "_unclassified"

// checkNoSlugCollisions fails closed (F2 review finding, 2026-07-16) when
// two DISTINCT skill Names -- or two distinct non-empty Metadata.Domain
// values -- slugify (model.go's slugify) to the SAME filesystem slug, OR
// (Finding 1, round 5, 2026-07-16) when a non-empty Metadata.Domain slugifies
// to the reservedUnclassifiedDomainSlug writeByDomain (generate.go) already
// uses for its own "_unclassified.md" bucket page. writeSkillPages/
// writeByDomain (generate.go) key every generated file path purely off
// NameSlug/DomainSlug, so an undetected collision would silently overwrite
// one skill's/one domain's generated page with another's (e.g. skills.name
// values "foo.bar" and "foo_bar" both slugify to "foo_bar", skills.name has
// NO charset constraint beyond TEXT UNIQUE) -- the exact DESIGN.md §6
// golden-bad class this generator's OTHER defensive checks
// (verifyNoDanglingEdges, the empty-Name check above) already guard against
// for different failure shapes; this closes the slug-collision shape (both
// the between-two-real-domains shape, and the real-domain-vs-reserved-bucket
// shape).
func checkNoSlugCollisions(records []skillRecord) error {
	nameBySlug := make(map[string]string, len(records))
	for _, r := range records {
		if prior, ok := nameBySlug[r.NameSlug]; ok && prior != r.Skill.Name {
			return fmt.Errorf("%w: skill names %q and %q both slugify to the same filename %q -- "+
				"refusing to generate, one skill detail page would silently overwrite the other",
				ErrDefensiveCheck, prior, r.Skill.Name, r.NameSlug)
		}
		nameBySlug[r.NameSlug] = r.Skill.Name
	}

	domainBySlug := make(map[string]string, len(records))
	for _, r := range records {
		if r.DomainSlug == "" {
			continue
		}
		if r.DomainSlug == reservedUnclassifiedDomainSlug {
			return fmt.Errorf("%w: domain %q slugifies to %q, the filename writeByDomain reserves for its "+
				"own _unclassified bucket page -- refusing to generate, this domain's by-domain page would "+
				"silently collide with (and, depending on write order, be overwritten by or overwrite) the "+
				"unclassified bucket's page", ErrDefensiveCheck, r.Metadata.Domain, reservedUnclassifiedDomainSlug)
		}
		if prior, ok := domainBySlug[r.DomainSlug]; ok && prior != r.Metadata.Domain {
			return fmt.Errorf("%w: domains %q and %q both slugify to the same filename %q -- "+
				"refusing to generate, one by-domain page would silently overwrite the other",
				ErrDefensiveCheck, prior, r.Metadata.Domain, r.DomainSlug)
		}
		domainBySlug[r.DomainSlug] = r.Metadata.Domain
	}
	return nil
}

// dedupeDependentsByID removes duplicate reverse-dependency rows
// Store.GetDependents (internal/skill/graph.go) can return for a single
// logical dependent, now that migration 002 has widened the
// skill_dependencies primary key to (skill_id, depends_on, relation_type)
// (Finding 3, round 5, 2026-07-16): one (skill_id, depends_on) pair MAY carry
// more than one typed edge (e.g. skill A both `requires` AND `recommends`
// the SAME skill B). GetDependents' query joins `skill_dependencies` to
// `skills` with NO DISTINCT, so a dependent connected via two relation types
// is returned TWICE -- byte-identically, since both rows resolve to the
// exact same target skill -- and renderSkillDetail's Dependents loop
// (render.go) would render the identical "- [`name`](slug.md)" line twice.
//
// Fixed on the CATALOG side (this package) -- GetDependents is an EXISTING,
// exported Store method this package only ever CALLS, never modifies
// (doc.go's package-level "never modifies any existing file in internal/
// skill" contract; internal/skill/graph.go is intentionally left untouched).
//
// De-duplication keys on the skill's ID (uuid.UUID, its actual primary key)
// rather than its Name: two distinct live skill rows can never legally share
// a Name (skills.name is TEXT UNIQUE, model.go's slugify docstring), so ID
// and Name identify the same equivalence class here, but keying on ID needs
// no assumption about that separate uniqueness constraint holding -- it is
// correct-by-construction off the row's own primary key.
//
// GetDependents' own SQL already orders by s.name (graph.go: `ORDER BY
// s.name`), so duplicate rows for the SAME dependent are already adjacent in
// the input slice; this function preserves that existing sorted order
// across the dedup pass (first-seen-per-ID wins, every later duplicate
// dropped) rather than re-sorting, since the input is already correctly
// ordered.
//
// The Dependencies direction (the forward edge, groupAndSortDeps below) does
// NOT need the same fix: it buckets edges BY RELATION TYPE
// (canonicalRelationOrder, model.go) before rendering, so a dependency
// connected via TWO relation types renders under TWO DIFFERENT subsection
// headings (e.g. once under "### Requires", once under "### Recommends") --
// that is the intended, non-duplicate rendering of two semantically distinct
// edges, not this defect's shape. The widened (skill_id, depends_on,
// relation_type) primary key guarantees at most ONE edge per
// (target, relation_type) pair, so within a SINGLE relation-type subsection
// no duplicate can occur; verified against store.go's GetByName depsSQL,
// which is keyed by that exact three-column PK and therefore returns at most
// one row per (skill_id, depends_on, relation_type) triple.
func dedupeDependentsByID(dependents []models.Skill) []models.Skill {
	if len(dependents) < 2 {
		return dependents
	}
	seen := make(map[uuid.UUID]bool, len(dependents))
	out := make([]models.Skill, 0, len(dependents))
	for _, d := range dependents {
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		out = append(out, d)
	}
	return out
}

// groupAndSortDeps buckets deps by RelationType and sorts each bucket by
// DependsOnName ascending (DESIGN.md §3.2's dependency-ordering rule).
// Store.GetByName's own SQL already orders by (relation_type alphabetical,
// sort_order NULLS LAST, name) -- alphabetical relation_type order is NOT
// the six-type CANONICAL order this catalog renders in (model.go's
// canonicalRelationOrder), so callers must re-bucket by type and iterate the
// canonical order themselves; this function performs that re-bucketing.
func groupAndSortDeps(deps []models.SkillDependency) map[models.DependencyType][]models.SkillDependency {
	byType := make(map[models.DependencyType][]models.SkillDependency)
	for _, d := range deps {
		byType[d.RelationType] = append(byType[d.RelationType], d)
	}
	for relType, list := range byType {
		list := list
		sort.Slice(list, func(i, j int) bool { return list[i].DependsOnName < list[j].DependsOnName })
		byType[relType] = list
	}
	return byType
}

// verifyNoDanglingEdges cross-checks the RAW (un-joined) skill_dependencies
// row count for a skill against the joined-and-therefore-target-must-exist
// count Store.GetByName already returned, via Store.Pool() (an EXISTING
// exported accessor, internal/skill/store.go:69 -- no store.go edit). A
// mismatch means at least one skill_dependencies row's depends_on no longer
// resolves to a live skills row -- GetByName's INNER JOIN silently DROPS
// such a row rather than erroring (DESIGN.md §6 golden-bad fixture (a));
// this generator must not inherit that silence.
func verifyNoDanglingEdges(ctx context.Context, store *skill.Store, sk models.Skill) error {
	var rawCount int
	err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM skill_dependencies WHERE skill_id = $1`, sk.ID).Scan(&rawCount)
	if err != nil {
		return fmt.Errorf("count raw dependency edges for skill %q: %w", sk.Name, err)
	}
	if rawCount != len(sk.Dependencies) {
		return fmt.Errorf("%w: skill %q has %d raw dependency edge(s) but only %d resolve to a live target skill -- dangling edge detected",
			ErrDefensiveCheck, sk.Name, rawCount, len(sk.Dependencies))
	}
	return nil
}
