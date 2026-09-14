package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// TOML Import / Export
// ---------------------------------------------------------------------------

// ImportFromTOML parses a TOML skill definition and creates the skill along with
// its dependencies and resources in a single transaction. When a query-side
// embedder is configured (Store.WithEmbedder) it also write-through embeds the
// new skill immediately after the transaction commits (§G59 F1) -- this is the
// function the live MCP skill_create tool calls directly (internal/mcp/tools.go),
// so it must carry the same embedding write-through as Store.Create/
// CreateFromTOML or a skill created via the deployed MCP tool would be
// invisible to vector-KNN.
func (s *Store) ImportFromTOML(ctx context.Context, tomlData []byte) (*models.Skill, error) {
	var wrapper models.TOMLSkillWrapper
	// G07 strict-decode (research/g06_g07_skilltree_dag_design.md §2.2/§2.3(4)):
	// decode via toml.Decode (not toml.Unmarshal) so the MetaData is retained
	// and the undecoded-key set can be inspected. A typo'd edge/resource key —
	// e.g. `requiress = [...]` under [skill.dependencies], or an extra field on
	// a [[skill.resources]] table — otherwise decodes into NOTHING and is
	// SILENTLY DROPPED (the exact silent-loss class G07 exists to close),
	// because BurntSushi never errors on an unmapped key. tomlUnmappedEdgeOrResourceKeys
	// scopes the hard-error to the edge/resource/component containers ONLY
	// (`skill.dependencies|resources|components.*`); other unmapped keys are
	// intentionally left tolerated — notably a top-level `skill.status`, which
	// is a DELIBERATELY-ignored key that keeps the live MCP skill_create path
	// fail-closed to `draft` (regression-guarded by
	// internal/mcp/skill_create_draft_test.go), and `skill.metadata.*`, a soft
	// blob whose stray keys are not an edge/resource-loss vector.
	md, err := toml.Decode(string(tomlData), &wrapper)
	if err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	if dropped := tomlUnmappedEdgeOrResourceKeys(md); len(dropped) > 0 {
		return nil, fmt.Errorf("%w: unmapped dependency/resource TOML keys would be silently dropped: %v (a typo'd edge/resource key must be a hard error, never a silent drop — design §2.2/§2.3(4))", ErrInvalidSkill, dropped)
	}

	// Validate required fields
	if wrapper.Skill.Name == "" {
		return nil, fmt.Errorf("%w: skill.name is required", ErrInvalidSkill)
	}
	if wrapper.Skill.Title == "" {
		return nil, fmt.Errorf("%w: skill.title is required", ErrInvalidSkill)
	}
	if wrapper.Skill.Content == "" {
		return nil, fmt.Errorf("%w: skill.content is required", ErrInvalidSkill)
	}

	// G07 part_of inversion (HXC-159 T-P3.01.5 closes A6; G07 follow-up
	// now WIRED). part_of is the ONLY alias whose documented semantics are
	// an INVERTED, CROSS-SKILL edge — the child declares its parent, meaning
	// "parent composes child" (a parent→child edge on a DIFFERENT skill than
	// the one being imported; models/skill.go TOMLDependencies.PartOf,
	// research/g06_g07_skilltree_dag_design.md §2.3(4),
	// research/skill_granularity_and_composition.md §4.1 alias table).
	// The inversion below resolves each named parent, folds duplicates, and
	// persists one parent→child `composes` edge per parent in this same
	// transaction — with the PARENT's cycle check and the no-partial-persist
	// contract (an absent parent is a hard ErrDependencyNotFound; a cycle is
	// a hard ErrCycleDetected; either aborts the whole import).
	// Silently ignoring a non-empty part_of would drop the edge with a
	// "successful" import — precisely the silent-loss failure G07 closes.
	var partOfParents []string
	{
		seen := make(map[string]bool)
		for _, name := range wrapper.Skill.Dependencies.PartOf {
			if name != "" && !seen[name] {
				seen[name] = true
				partOfParents = append(partOfParents, name)
			}
		}
	}

	// Check if skill already exists
	existing, err := s.GetByName(ctx, wrapper.Skill.Name)
	if err != nil && !errors.Is(err, ErrSkillNotFound) {
		return nil, fmt.Errorf("check existing skill: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrSkillExists, wrapper.Skill.Name)
	}

	// Marshal metadata
	metadata := models.SkillMetadata{
		Tags:       wrapper.Skill.Metadata.Tags,
		Domain:     wrapper.Skill.Metadata.Domain,
		Complexity: wrapper.Skill.Metadata.Complexity,
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	skill := &models.Skill{
		ID:          uuid.New(),
		Name:        wrapper.Skill.Name,
		Version:     wrapper.Skill.Version,
		Title:       wrapper.Skill.Title,
		Description: wrapper.Skill.Description,
		Content:     wrapper.Skill.Content,
		Metadata:    metadataJSON,
		Status:      models.SkillStatusDraft,
		Kind:        models.SkillKind(wrapper.Skill.Kind).NormalizeOrAtomic(),
	}

	// Resolve dependency names to IDs. The batch lookup covers EVERY name any
	// edge form references — the six canonical relation types, the
	// same-direction aliases (depends_on/prerequisite → requires), and the
	// [[skill.components]] ergonomic form — so a single query populates the
	// resolution map for all of them (G07, research/g06_g07_skilltree_dag_design.md §2.2).
	depNames := collectDepNames(wrapper.Skill.Dependencies, wrapper.Skill.Components)
	depNameToID := make(map[string]uuid.UUID)

	if len(depNames) > 0 {
		// Query for existing skills that match dependency names
		rows, err := s.pool.Query(ctx, `
			SELECT id, name FROM skills WHERE name = ANY($1)
		`, depNames)
		if err != nil {
			return nil, fmt.Errorf("resolve dependency names: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan dep name: %w", err)
			}
			depNameToID[name] = id
		}
		rows.Close()
	}

	// Build dependency records. addEdges resolves a list of target names for a
	// single relation type: a hard-closure edge (requires/extends/composes)
	// whose target is absent is a HARD error (no silent partial import,
	// design §2.3(4)); an advisory edge (recommends/related_to/alternative_to)
	// with an absent target is soft-skipped.
	//
	// F5(iii) — DIVERGENCE from design §2.3(4), documented rationale
	// (§11.4.6): §2.3(4) reads "any edge ... that cannot be persisted ...
	// aborts the import". This code aborts for HARD-closure edges but
	// SOFT-SKIPS advisory ones. The rationale is deliberate: advisory edges
	// (recommends/related_to/alternative_to) are "see also" hints that are, by
	// design, NOT part of the hard closure and NOT acyclicity-enforced
	// (models.IsHardClosure / skill_granularity_and_composition.md §4.1); a
	// recommendation pointing at a skill that simply is not modelled yet is a
	// normal, non-fatal state, and hard-erroring on it would make importing a
	// well-formed skill fail on an advisory pointer to an as-yet-unwritten
	// skill. This also preserves the pre-G07 `recommends` behaviour (which
	// already soft-skipped a missing target), generalised to the full advisory
	// set. The NO-SILENT-LOSS guarantee §2.3(4) protects is fully upheld for
	// every edge that IS part of the required closure (requires/extends/
	// composes) — those still hard-error. The residual boundary: an advisory
	// edge to a missing target is dropped without error; that is intentional,
	// not a silent hard-closure loss.
	var depsToCreate []tomlDepEdge
	addEdges := func(names []string, rel models.DependencyType) error {
		hard := models.IsHardClosure(rel)
		for _, name := range names {
			id, ok := depNameToID[name]
			if !ok {
				if hard {
					return fmt.Errorf("%w: %s dependency %q not found", ErrDependencyNotFound, rel, name)
				}
				continue // advisory: soft-skip a missing target
			}
			depsToCreate = append(depsToCreate, tomlDepEdge{targetID: id, relationType: rel, name: name})
		}
		return nil
	}

	deps := wrapper.Skill.Dependencies
	// requires + the same-direction aliases fold into `requires`
	// (skill_granularity_and_composition.md §4.1 alias table).
	for _, list := range [][]string{deps.Requires, deps.DependsOn, deps.Prerequisite} {
		if err := addEdges(list, models.DepTypeRequires); err != nil {
			return nil, err
		}
	}
	if err := addEdges(deps.Extends, models.DepTypeExtends); err != nil {
		return nil, err
	}
	if err := addEdges(deps.Recommends, models.DepTypeRecommends); err != nil {
		return nil, err
	}
	if err := addEdges(deps.Composes, models.DepTypeComposes); err != nil {
		return nil, err
	}
	if err := addEdges(deps.RelatedTo, models.DepTypeRelatedTo); err != nil {
		return nil, err
	}
	if err := addEdges(deps.Alternative, models.DepTypeAlternative); err != nil {
		return nil, err
	}

	// [[skill.components]] — the ergonomic umbrella→component authoring form —
	// each materializes as one `composes` edge carrying its ordering/optionality
	// (skill_granularity_and_composition.md §5.1; a component is hard-closure,
	// so an absent target is a hard error).
	for _, comp := range wrapper.Skill.Components {
		id, ok := depNameToID[comp.Name]
		if !ok {
			return nil, fmt.Errorf("%w: composes component %q not found", ErrDependencyNotFound, comp.Name)
		}
		order := comp.Order
		depsToCreate = append(depsToCreate, tomlDepEdge{
			targetID:     id,
			relationType: models.DepTypeComposes,
			name:         comp.Name,
			optional:     comp.Optional,
			sortOrder:    &order,
		})
	}

	// G07 idempotent edge fold (F2; research/g06_g07_skilltree_dag_design.md
	// §2.3(3)): collapse duplicate (targetID, relationType) edges to a single
	// edge BEFORE persisting. The stored PK is (skill_id, depends_on,
	// relation_type) (migrations/002_granularity.up.sql:35-38), so a repeated
	// target for one relation type — a repeated name in a single list, the same
	// name via `requires` and its alias `depends_on`/`prerequisite` (all fold
	// to `requires`), OR the same target named both in `composes = [...]` and a
	// [[skill.components]] entry — would otherwise hit that PK and abort the
	// whole import with a raw Postgres `duplicate key ... 23505`. The design
	// mandates folding to ONE edge, not aborting. When a duplicate is found we
	// keep the FIRST occurrence but PREFER the richer attribute carrier: a
	// [[skill.components]] edge (non-nil sortOrder) supersedes a plain
	// composes-list edge (nil sortOrder) for the same target, so its
	// ordering/optionality is never lost to the fold regardless of authoring
	// order.
	depsToCreate = dedupDepEdges(depsToCreate)

	// Resolve part_of parents to IDs. `composes` is hard-closure, so an
	// absent parent is a HARD error (no silent partial import, design
	// §2.3(4)) — the no-partial-persist contract the old F1 guard enforced
	// now holds for the wired path instead of via a blanket refusal.
	type invertedEdge struct {
		parentID   uuid.UUID
		parentName string
	}
	var invertedToCreate []invertedEdge
	for _, name := range partOfParents {
		id, ok := depNameToID[name]
		if !ok {
			return nil, fmt.Errorf("%w: part_of parent %q not found", ErrDependencyNotFound, name)
		}
		invertedToCreate = append(invertedToCreate, invertedEdge{parentID: id, parentName: name})
	}

	// Build resource records
	resources := make([]models.Resource, len(wrapper.Skill.Resources))
	for i, r := range wrapper.Skill.Resources {
		resources[i] = models.Resource{
			ID:           uuid.New(),
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		}
	}

	// Execute everything in a transaction
	if err := s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Insert skill
		_, err := tx.Exec(ctx, `
			INSERT INTO skills (id, name, version, title, description, content, metadata, status, kind, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		`, skill.ID, skill.Name, skill.Version, skill.Title, skill.Description, skill.Content, skill.Metadata, skill.Status, skill.Kind)
		if err != nil {
			return fmt.Errorf("insert skill: %w", err)
		}

		// Initialize registry entry
		_, err = tx.Exec(ctx, `
			INSERT INTO skill_registry (skill_id, skill_name, missing_deps, stale, last_review, auto_expand, coverage)
			VALUES ($1, $2, '{}', false, NOW(), true, 0.0)
		`, skill.ID, skill.Name)
		if err != nil {
			return fmt.Errorf("insert registry entry: %w", err)
		}

		// Insert dependencies. Cycle detection applies ONLY to hard-closure
		// relations (requires/composes/extends); advisory relations
		// (recommends/related_to/alternative_to) are exempt — they MAY cycle by
		// nature and their symmetric back-edges must not be rejected as cycles
		// (skill_granularity_and_composition.md §4.1; mirrors graph.go
		// AddDependency's NEW-1 fix).
		for _, dep := range depsToCreate {
			if models.IsHardClosure(dep.relationType) {
				cycle, err := hasCycle(ctx, tx, skill.ID, dep.targetID)
				if err != nil {
					return fmt.Errorf("cycle check for %s: %w", dep.name, err)
				}
				if cycle {
					return fmt.Errorf("%w: adding dependency on %s would create cycle", ErrCycleDetected, dep.name)
				}
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional, sort_order)
				VALUES ($1, $2, $3, $4, $5)
			`, skill.ID, dep.targetID, dep.relationType, dep.optional, dep.sortOrder)
			if err != nil {
				return fmt.Errorf("insert dependency %s: %w", dep.name, err)
			}
		}

		// Insert part_of inverted edges: one parent→child `composes` edge per
		// resolved parent (HXC-159 T-P3.01.5). `composes` is hard-closure, so
		// the PARENT's cycle check runs here — adding parent→child must not
		// close a loop the child reaches back through — and the parent's
		// registry state is recalculated as the edge source (mirrors
		// AddDependency's source-side recalc).
		for _, inv := range invertedToCreate {
			cycle, err := hasCycle(ctx, tx, inv.parentID, skill.ID)
			if err != nil {
				return fmt.Errorf("cycle check for part_of parent %s: %w", inv.parentName, err)
			}
			if cycle {
				return fmt.Errorf("%w: part_of parent %s would create cycle", ErrCycleDetected, inv.parentName)
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional, sort_order)
				VALUES ($1, $2, $3, false, NULL)
			`, inv.parentID, skill.ID, models.DepTypeComposes)
			if err != nil {
				return fmt.Errorf("insert part_of edge on parent %s: %w", inv.parentName, err)
			}
			if err := s.recalcMissingDeps(ctx, tx, inv.parentID); err != nil {
				return fmt.Errorf("recalc missing deps for part_of parent %s: %w", inv.parentName, err)
			}
		}

		// Insert resources
		for _, r := range resources {
			r.SkillID = skill.ID
			_, err := tx.Exec(ctx, `
				INSERT INTO resources (id, skill_id, url, title, resource_type, created_at)
				VALUES ($1, $2, $3, $4, $5, NOW())
			`, r.ID, r.SkillID, r.URL, r.Title, r.ResourceType)
			if err != nil {
				return fmt.Errorf("insert resource %s: %w", r.URL, err)
			}
		}

		// Recalculate registry state
		if err := s.recalcMissingDeps(ctx, tx, skill.ID); err != nil {
			return fmt.Errorf("recalc missing deps: %w", err)
		}
		if err := s.recalcCoverage(ctx, tx, skill.ID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		// Audit log
		if err := s.logAudit(ctx, tx, "skill.imported", &skill.ID, map[string]interface{}{
			"name":            skill.Name,
			"version":         skill.Version,
			"deps_count":      len(depsToCreate),
			"part_of_count":   len(invertedToCreate),
			"resources_count": len(resources),
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		// Set runtime fields on the returned skill
		skill.Dependencies = make([]models.SkillDependency, 0, len(depsToCreate))
		for _, dep := range depsToCreate {
			skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
				SkillID:       skill.ID,
				DependsOn:     dep.targetID,
				RelationType:  dep.relationType,
				Optional:      dep.optional,
				SortOrder:     dep.sortOrder,
				DependsOnName: dep.name,
			})
		}
		skill.Resources = resources

		return nil
	}); err != nil {
		return nil, err
	}

	// §G59 F1 (code-review BLOCKING remediation): the LIVE, deployed MCP
	// skill_create tool (internal/mcp/tools.go registerSkillCreate) calls
	// ImportFromTOML DIRECTLY -- never Store.Create -- so without this call
	// every skill created through the deployed MCP tool landed with a NULL
	// embedding column regardless of whether a query-side embedder was
	// configured, invisible to the vector-KNN leg of hybrid Search: the exact
	// defect class §G59 exists to close, now proven closed for the path
	// end-users actually exercise (see
	// TestG59_ImportFromTOML_WritesEmbedding_RetrievableByVectorKNN).
	// Called AFTER the transaction above has durably committed the skill row
	// (mirrors Create's own post-commit embedWriteThrough call and its
	// degrade-gracefully posture, see that method's doc comment in store.go):
	// an embedding-provider outage degrades ONLY this one skill to
	// trigram-only searchability rather than rolling back or blocking the
	// import. A nil embedder (the default -- no query-side embedder
	// configured) is a documented no-op, identical to Create.
	if s.embedder != nil {
		s.embedWriteThrough(ctx, skill)
	}

	return skill, nil
}

// ExportToTOML exports a skill and its dependencies and resources as TOML.
func (s *Store) ExportToTOML(ctx context.Context, skillName string) ([]byte, error) {
	skill, err := s.GetByName(ctx, skillName)
	if err != nil {
		return nil, err
	}

	// Parse metadata for TOML
	var meta models.SkillMetadata
	if len(skill.Metadata) > 0 {
		if err := json.Unmarshal(skill.Metadata, &meta); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	// Build TOML wrapper
	wrapper := models.TOMLSkillWrapper{
		Skill: models.TOMLSkillDef{
			Name:        skill.Name,
			Version:     skill.Version,
			Title:       skill.Title,
			Description: skill.Description,
			Content:     skill.Content,
			// W3 fix (Fable code-review remediation, P1.T1): Kind was never
			// set here, so exporting an umbrella/composite skill and
			// re-importing it silently downgraded it to 'atomic' (the
			// column DEFAULT ImportFromTOML/CreateFromTOML fall back to via
			// SkillKind.NormalizeOrAtomic() when Kind is empty) — Kind never
			// survived an export->import round-trip.
			Kind:      string(skill.Kind),
			Metadata:  meta,
			Resources: make([]models.TOMLResource, len(skill.Resources)),
		},
	}

	// Categorize dependencies by relation type
	for _, dep := range skill.Dependencies {
		depName := dep.DependsOnName
		if depName == "" {
			// G33: resolve the name from the edge's target ID when the
			// GetByName JOIN did not populate DependsOnName. The scan error
			// MUST be propagated — the pre-G33 `_ = ...Scan(&name)` swallowed
			// it, so a transient DB/context error (or an unresolvable target)
			// silently left name empty and emitted a blank dependency name
			// into the exported TOML.
			var name string
			if err := s.pool.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, dep.DependsOn).Scan(&name); err != nil {
				return nil, fmt.Errorf("resolve dependency name for target %s: %w", dep.DependsOn, err)
			}
			depName = name
		}
		// G33: never emit an empty dependency edge name. A blank name is
		// export corruption regardless of how it arose (a swallowed scan
		// error above, or a genuinely empty skills.name) — it produces a
		// `requires = [""]`-class edge that re-imports wrong. Fail the export
		// loudly instead (§11.4.6 / §11.4.201: assert the real condition,
		// never write a bluffed empty edge).
		if depName == "" {
			return nil, fmt.Errorf("dependency target %s has no resolvable name; refusing to export an empty dependency edge", dep.DependsOn)
		}

		// G07: emit every canonical relation type (the six-type typed-edge set
		// from the P1.T1 granularity model) so an export→import→export
		// round-trip is edge-name-stable. Under the pre-G07 code only
		// requires/extends/recommends were emitted, silently dropping
		// composes/related_to/alternative_to on export.
		switch dep.RelationType {
		case models.DepTypeRequires:
			wrapper.Skill.Dependencies.Requires = append(wrapper.Skill.Dependencies.Requires, depName)
		case models.DepTypeExtends:
			wrapper.Skill.Dependencies.Extends = append(wrapper.Skill.Dependencies.Extends, depName)
		case models.DepTypeRecommends:
			wrapper.Skill.Dependencies.Recommends = append(wrapper.Skill.Dependencies.Recommends, depName)
		case models.DepTypeComposes:
			// G07 (F3; research/g06_g07_skilltree_dag_design.md §2.3(1)+(3)): a
			// composes edge that carries ordering/optionality (imported via a
			// [[skill.components]] entry) MUST export back through the SAME
			// carrier, or the optional/sort_order attrs are silently lost on
			// export and the round-trip is not attribute-stable. A plain
			// composes edge (no attrs — e.g. added via Store.AddDependency)
			// stays in the `composes = [...]` list. The discriminator matches
			// the import shape: only [[skill.components]] can set these attrs.
			if dep.SortOrder != nil || dep.Optional {
				order := 0
				if dep.SortOrder != nil {
					order = *dep.SortOrder
				}
				wrapper.Skill.Components = append(wrapper.Skill.Components, models.TOMLComponent{
					Name:     depName,
					Order:    order,
					Optional: dep.Optional,
				})
			} else {
				wrapper.Skill.Dependencies.Composes = append(wrapper.Skill.Dependencies.Composes, depName)
			}
		case models.DepTypeRelatedTo:
			wrapper.Skill.Dependencies.RelatedTo = append(wrapper.Skill.Dependencies.RelatedTo, depName)
		case models.DepTypeAlternative:
			wrapper.Skill.Dependencies.Alternative = append(wrapper.Skill.Dependencies.Alternative, depName)
		}
	}

	// Map resources
	for i, r := range skill.Resources {
		wrapper.Skill.Resources[i] = models.TOMLResource{
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		}
	}

	// Encode to TOML
	var buf bytes.Buffer
	buf.WriteString("# Skill definition exported from HelixKnowledge\n")
	buf.WriteString(fmt.Sprintf("# Skill: %s (v%s)\n", skill.Name, skill.Version))
	buf.WriteString(fmt.Sprintf("# Exported at: %s\n\n", skill.UpdatedAt.Format("2006-01-02T15:04:05Z")))

	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(wrapper); err != nil {
		return nil, fmt.Errorf("encode TOML: %w", err)
	}

	return buf.Bytes(), nil
}

// tomlDepEdge is one resolved dependency edge staged for insertion during a
// TOML import (target skill ID + relation type + carried attributes). name is
// retained for error reporting and for populating the returned skill's runtime
// DependsOnName field.
type tomlDepEdge struct {
	targetID     uuid.UUID
	relationType models.DependencyType
	name         string
	optional     bool
	sortOrder    *int
}

// collectDepNames gathers every dependency target name referenced by a TOML
// skill definition across ALL edge forms — the six canonical relation types,
// the same-direction aliases (depends_on/prerequisite), and the
// [[skill.components]] ergonomic form — de-duplicated, so the caller can
// resolve them all in a single batch lookup (G07).
func collectDepNames(deps models.TOMLDependencies, components []models.TOMLComponent) []string {
	seen := make(map[string]bool)
	var names []string
	add := func(list []string) {
		for _, n := range list {
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}

	add(deps.Requires)
	add(deps.Extends)
	add(deps.Recommends)
	add(deps.Composes)
	add(deps.RelatedTo)
	add(deps.Alternative)
	add(deps.DependsOn)
	add(deps.Prerequisite)
	add(deps.PartOf)
	for _, c := range components {
		add([]string{c.Name})
	}

	return names
}

// tomlUnmappedEdgeOrResourceKeys returns the dotted string form of every
// undecoded TOML key that lands under an EDGE/RESOURCE container —
// `skill.dependencies.*`, `skill.resources.*`, or `skill.components.*` — i.e.
// the containers whose silent drop is a G07 data-loss defect (a typo'd
// dependency key decoding into zero edges; an unknown field on a resource/
// component table). Undecoded keys OUTSIDE those containers are intentionally
// tolerated: `skill.status` is a deliberately-ignored key (the MCP
// skill_create fail-closed-to-draft contract,
// internal/mcp/skill_create_draft_test.go) and `skill.metadata.*` is a soft
// blob, neither an edge/resource-loss vector. The key segment names come from
// BurntSushi/toml v1.6.0 MetaData.Undecoded() (captured: a typo'd dep key
// reports ["skill" "dependencies" "<key>"], an extra resource field reports
// ["skill" "resources" "<key>"]).
func tomlUnmappedEdgeOrResourceKeys(md toml.MetaData) []string {
	var dropped []string
	for _, k := range md.Undecoded() {
		if len(k) >= 2 && k[0] == "skill" {
			switch k[1] {
			case "dependencies", "resources", "components":
				dropped = append(dropped, k.String())
			}
		}
	}
	return dropped
}

// dedupDepEdges folds duplicate (targetID, relationType) edges to a single
// edge so a TOML import is idempotent instead of aborting on the
// (skill_id, depends_on, relation_type) primary key (F2, see the call site).
// First occurrence order is preserved; when a later duplicate carries edge
// attributes (a non-nil sortOrder, i.e. a [[skill.components]] entry) and the
// kept edge does not, the kept edge is upgraded with the richer
// optional/sortOrder so component ordering/optionality survives the fold
// regardless of authoring order.
func dedupDepEdges(edges []tomlDepEdge) []tomlDepEdge {
	type edgeKey struct {
		target uuid.UUID
		rel    models.DependencyType
	}
	seen := make(map[edgeKey]int, len(edges))
	out := make([]tomlDepEdge, 0, len(edges))
	for _, e := range edges {
		k := edgeKey{target: e.targetID, rel: e.relationType}
		if idx, ok := seen[k]; ok {
			if e.sortOrder != nil && out[idx].sortOrder == nil {
				out[idx].optional = e.optional
				out[idx].sortOrder = e.sortOrder
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, e)
	}
	return out
}
