package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Dependency graph operations
// ---------------------------------------------------------------------------

// AddDependency adds a directed edge from skillID to dependsOn with cycle detection.
// The relation type must be one of: requires, extends, recommends, composes,
// related_to, alternative_to (research/skill_granularity_and_composition.md §4.1).
func (s *Store) AddDependency(ctx context.Context, skillID, dependsOn uuid.UUID, relType models.DependencyType) error {
	if skillID == dependsOn {
		return fmt.Errorf("%w: self-referencing dependency", ErrCycleDetected)
	}

	validTypes := map[models.DependencyType]bool{
		models.DepTypeRequires:    true,
		models.DepTypeExtends:     true,
		models.DepTypeRecommends:  true,
		models.DepTypeComposes:    true, // NEW (R16) — hard closure, whole->part
		models.DepTypeRelatedTo:   true, // NEW (R16) — advisory, symmetric
		models.DepTypeAlternative: true, // NEW (R16) — advisory, symmetric
	}
	if !validTypes[relType] {
		return fmt.Errorf("%w: invalid relation type %q", ErrInvalidSkill, relType)
	}

	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Verify both skills exist
		var fromName, toName string
		if err := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, skillID).Scan(&fromName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: source skill %s", ErrSkillNotFound, skillID)
			}
			return fmt.Errorf("check source skill: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, dependsOn).Scan(&toName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: target skill %s", ErrDependencyNotFound, dependsOn)
			}
			return fmt.Errorf("check target skill: %w", err)
		}

		// Check for existing edge. W2(a) fix (Fable code-review remediation,
		// P1.T1): scoped to the (skill_id, depends_on, relation_type)
		// TRIPLE, matching the widened 002_granularity PK
		// (skill_dependencies_pkey now (skill_id, depends_on, relation_type)
		// -- see migrations/002_granularity.up.sql step (4) and
		// store.Create's own ON CONFLICT (skill_id, depends_on,
		// relation_type) target). The previous (skill_id, depends_on)-only
		// check wrongly rejected a SECOND typed edge on an
		// already-related pair (e.g. adding a `recommends` edge once a
		// `requires` edge already exists for the same pair) as a duplicate,
		// even though the widened PK explicitly allows a pair to carry more
		// than one typed edge.
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2 AND relation_type = $3)
		`, skillID, dependsOn, relType).Scan(&exists); err != nil {
			return fmt.Errorf("check existing dependency: %w", err)
		}
		if exists {
			return fmt.Errorf("dependency already exists: %s -> %s (%s)", fromName, toName, relType)
		}

		// Cycle detection applies ONLY to hard-closure relations
		// (requires/composes/extends). Advisory relations
		// (recommends/related_to/alternative_to) are exempt -- they MAY cycle
		// by nature (research/skill_granularity_and_composition.md §4.1
		// "exempt (may cycle by nature)", §4.3, §5.4(5)). NEW-1 fix (Fable
		// code-review round-2): the hasCycle WALK was scoped to
		// HardClosureTypes (W2(b)), but the CALL was unconditional, so an
		// ADVISORY candidate edge whose REVERSE path is a hard edge (e.g.
		// parent--composes-->child then child--related_to-->parent) was
		// falsely rejected as a cycle -- the advisory/hard cell of the 2x2.
		if models.IsHardClosure(relType) {
			cycle, err := hasCycle(ctx, tx, skillID, dependsOn)
			if err != nil {
				return fmt.Errorf("cycle detection check: %w", err)
			}
			if cycle {
				return fmt.Errorf("%w: adding %s -> %s would create a cycle", ErrCycleDetected, fromName, toName)
			}
		}

		// Insert the edge
		_, err := tx.Exec(ctx, `
			INSERT INTO skill_dependencies (skill_id, depends_on, relation_type)
			VALUES ($1, $2, $3)
		`, skillID, dependsOn, relType)
		if err != nil {
			return fmt.Errorf("insert dependency: %w", err)
		}

		// Update registry: recalculate missing deps for the source skill
		if err := s.recalcMissingDeps(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc missing deps: %w", err)
		}

		// Audit log
		if err := s.logAudit(ctx, tx, "dependency.added", &skillID, map[string]interface{}{
			"from":          fromName,
			"to":            toName,
			"relation_type": relType,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// RemoveDependency removes a directed edge between two skills.
func (s *Store) RemoveDependency(ctx context.Context, skillID, dependsOn uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Get names for the audit log. G25 fix
		// (research/g18_g25_g26_correctness_bundle.md §2): capture each Scan
		// error EXPLICITLY instead of discarding it via `_ =`. When an endpoint
		// skill row is gone (the register's "a skill is already gone" case) the
		// lookup fails, and buildRemovalAuditDetail records an explicit
		// `from_lookup_error` / `to_lookup_error` marker rather than a bare
		// `"from":""` / `"to":""` that an operator cannot tell apart from a
		// skill whose name legitimately IS empty -- restoring the audit trail's
		// evidentiary value (R11). The removal stays best-effort: an
		// unavailable cosmetic name does NOT fail the transaction (the delete
		// may still be valid and desired). The AddDependency path already
		// propagates its Scan errors (graph.go:42/48); this brings
		// RemoveDependency's audit path to the same standard.
		var fromName, toName string
		fromErr := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, skillID).Scan(&fromName)
		toErr := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, dependsOn).Scan(&toName)

		cmdTag, err := tx.Exec(ctx, `
			DELETE FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2
		`, skillID, dependsOn)
		if err != nil {
			return fmt.Errorf("delete dependency: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("dependency not found: %s -> %s", skillID, dependsOn)
		}

		// Recalculate missing deps for the source skill
		if err := s.recalcMissingDeps(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc missing deps: %w", err)
		}

		// Audit log
		if err := s.logAudit(ctx, tx, "dependency.removed", &skillID,
			buildRemovalAuditDetail(fromName, fromErr, toName, toErr)); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// buildRemovalAuditDetail assembles the audit-detail map for a
// `dependency.removed` event from the best-effort endpoint name lookups. When a
// lookup Scan failed (fromErr / toErr non-nil -- e.g. the skill row was already
// gone), the corresponding endpoint is recorded as an explicit
// `from_lookup_error` / `to_lookup_error` marker instead of a silently-empty
// `"from"` / `"to"` string that an operator could not distinguish from a skill
// whose name legitimately is empty. Pure + side-effect-free so the not-found
// audit path is unit-testable without a live pgx.Tx (mirrors this package's
// collectDepNames idiom in import_export.go). G25 fix, see
// research/g18_g25_g26_correctness_bundle.md §2.
func buildRemovalAuditDetail(fromName string, fromErr error, toName string, toErr error) map[string]interface{} {
	detail := map[string]interface{}{}
	if fromErr != nil {
		detail["from_lookup_error"] = fromErr.Error()
	} else {
		detail["from"] = fromName
	}
	if toErr != nil {
		detail["to_lookup_error"] = toErr.Error()
	} else {
		detail["to"] = toName
	}
	return detail
}

// recalcMissingDeps recalculates and updates the missing_deps array for a skill.
func (s *Store) recalcMissingDeps(ctx context.Context, tx pgx.Tx, skillID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE skill_registry
		SET missing_deps = COALESCE((
			SELECT array_agg(s2.name ORDER BY s2.name)
			FROM skill_dependencies sd
			JOIN skills s2 ON s2.id = sd.depends_on
			WHERE sd.skill_id = $1
			  AND NOT EXISTS (
				  SELECT 1 FROM skills s3
				  WHERE s3.id = sd.depends_on
				  AND s3.status IN ('validated', 'active')
			  )
		), '{}')
		WHERE skill_id = $1
	`, skillID)

	return err
}

// hasCycle performs a DFS from toID to see if it can reach fromID.
// If so, adding an edge fromID -> toID would create a cycle.
//
// W2(b) fix (Fable code-review remediation, P1.T1): the reachability walk
// is scoped to models.HardClosureTypes (requires/composes/extends) --
// research/skill_granularity_and_composition.md §4.2 defines these as the
// ONLY relation types the "everything needed for X" transitive resolver
// walks, and correspondingly the only types the structural-acyclicity
// invariant applies to (mirrored by seed/validate_dag.py's
// HARD_CLOSURE_RELATIONS). recommends/related_to/alternative_to are
// advisory and, per that same doc, related_to/alternative_to are
// explicitly SYMMETRIC "see also"/"substitute" relations -- a forward edge
// A->B and its reciprocal back-edge B->A are the SAME relationship
// recorded from both sides, never a structural cycle. Before this fix the
// walk was relation-type-agnostic (it followed every skill_dependencies
// row regardless of type), so adding the forward related_to/alternative_to
// edge A->B made B falsely "reach" A for the purposes of THIS check,
// causing the reciprocal B->A edge to be wrongly rejected with
// ErrCycleDetected. Scoping the walk to the hard-closure set fixes the
// false positive for advisory back-edges while still rejecting a genuine
// hard-closure cycle (e.g. two composes edges forming a loop), because
// composes/requires/extends edges remain fully part of the walk.
func hasCycle(ctx context.Context, tx pgx.Tx, fromID, toID uuid.UUID) (bool, error) {
	// Use a PostgreSQL recursive CTE to check reachability from toID to
	// fromID, following ONLY hard-closure-typed edges.
	var reachable bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE reach AS (
			SELECT depends_on AS id
			FROM skill_dependencies
			WHERE skill_id = $1
			  AND relation_type = ANY($3)

			UNION

			SELECT sd.depends_on
			FROM skill_dependencies sd
			INNER JOIN reach r ON r.id = sd.skill_id
			WHERE sd.relation_type = ANY($3)
		)
		SELECT EXISTS(SELECT 1 FROM reach WHERE id = $2)
	`, toID, fromID, hardClosureRelationTypeStrings()).Scan(&reachable)

	return reachable, err
}

// hardClosureRelationTypeStrings converts models.HardClosureTypes to a
// plain []string for pgx `= ANY($n)` array binding -- pgx v5's default type
// map does not automatically encode a []models.DependencyType (a slice of a
// named string type) as a TEXT[] array parameter.
func hardClosureRelationTypeStrings() []string {
	out := make([]string, len(models.HardClosureTypes))
	for i, t := range models.HardClosureTypes {
		out[i] = string(t)
	}
	return out
}

// GetDependencyTree returns the full dependency tree starting from a root skill name,
// using a PostgreSQL recursive CTE. Results are limited to maxDepth levels.
func (s *Store) GetDependencyTree(ctx context.Context, rootName string, maxDepth int) (*models.SkillTreeNode, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 50 {
		maxDepth = 50 // Hard cap to prevent runaway queries
	}

	// First, get the root skill
	rootSkill, err := s.GetByName(ctx, rootName)
	if err != nil {
		return nil, err
	}

	// Use recursive CTE to get all transitive dependencies with depth info
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE dep_tree AS (
			SELECT
				s.id,
				s.name,
				s.version,
				s.title,
				s.description,
				s.content,
				s.metadata,
				s.status,
				s.kind,
				s.created_at,
				s.updated_at,
				sd.relation_type,
				0 AS depth
			FROM skill_dependencies sd
			JOIN skills s ON s.id = sd.depends_on
			WHERE sd.skill_id = $1

			UNION ALL

			SELECT
				s.id,
				s.name,
				s.version,
				s.title,
				s.description,
				s.content,
				s.metadata,
				s.status,
				s.kind,
				s.created_at,
				s.updated_at,
				sd.relation_type,
				dt.depth + 1
			FROM skill_dependencies sd
			JOIN skills s ON s.id = sd.depends_on
			JOIN dep_tree dt ON dt.id = sd.skill_id
			WHERE dt.depth + 1 < $2
		)
		SELECT id, name, version, title, description, content, metadata, status, kind, created_at, updated_at, relation_type, depth
		FROM dep_tree
		ORDER BY depth, name
	`, rootSkill.ID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("recursive dependency query: %w", err)
	}
	defer rows.Close()

	// Build a flat list of all nodes with their depth and relation type.
	// flatNode is declared at package scope (below) so the collectIDs helper
	// can accept a []flatNode.

	// Map: skill ID -> flatNode
	nodeMap := make(map[uuid.UUID]*flatNode)
	// Keep insertion order for building tree
	var flatNodes []flatNode

	for rows.Next() {
		var fn flatNode
		var metaJSON []byte
		if err := rows.Scan(
			&fn.skill.ID, &fn.skill.Name, &fn.skill.Version, &fn.skill.Title,
			&fn.skill.Description, &fn.skill.Content, &metaJSON,
			&fn.skill.Status, &fn.skill.Kind, &fn.skill.CreatedAt, &fn.skill.UpdatedAt,
			&fn.relationType, &fn.depth,
		); err != nil {
			return nil, fmt.Errorf("scan dep tree node: %w", err)
		}
		fn.skill.Metadata = metaJSON
		nodeMap[fn.skill.ID] = &fn
		flatNodes = append(flatNodes, fn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dep tree: %w", err)
	}

	// Build tree: root -> children
	root := &models.SkillTreeNode{
		Skill:    *rootSkill,
		Depth:    0,
		Children: []models.SkillTreeNode{},
	}

	// For building the tree, we need parent->child relationships. The CTE gives
	// us the closure (every reachable node, with depth) but not the edges, so we
	// re-query the edges among the closure nodes and assemble the tree from them.
	//
	// G06 fix (register GAPS_AND_RISKS_REGISTER.md §G06): the previous assembly
	// attached ONLY the root's direct children
	// (`root.Children = childrenMap[rootSkill.ID]`) and never linked
	// grandchildren, truncating the tree to depth-1. We instead build a
	// parent->childIDs adjacency and walk it recursively FROM the root, so every
	// reachable node is attached to its parent -- the full N-level tree.
	if len(flatNodes) > 0 {
		parentRows, err := s.pool.Query(ctx, `
			SELECT skill_id, depends_on
			FROM skill_dependencies
			WHERE depends_on = ANY($1)
			ORDER BY skill_id, sort_order NULLS LAST, depends_on
		`, collectIDs(flatNodes))
		if err != nil {
			return nil, fmt.Errorf("query parent relationships: %w", err)
		}
		defer parentRows.Close()

		// parentID -> ordered child IDs, restricted to nodes in the closure.
		childIDs := make(map[uuid.UUID][]uuid.UUID)
		for parentRows.Next() {
			var parentID, childID uuid.UUID
			if err := parentRows.Scan(&parentID, &childID); err != nil {
				return nil, fmt.Errorf("scan parent relationship: %w", err)
			}
			if _, ok := nodeMap[childID]; ok {
				childIDs[parentID] = append(childIDs[parentID], childID)
			}
		}
		if err := parentRows.Err(); err != nil {
			return nil, fmt.Errorf("iterate parent relationships: %w", err)
		}

		// Recursive, cycle-guarded assembly. `seen` is the load-bearing cycle
		// guard: the skill graph permits ADVISORY cycles
		// (recommends/related_to/alternative_to are exempt from hard-closure
		// acyclicity -- see AddDependency), so the closure edge set may contain a
		// cycle. Without `seen`, walking `childIDs` would recurse forever on such
		// a cycle (stack overflow). With it, every reachable node is attached
		// exactly once, under its first-resolved parent, and every cycle
		// terminates at its closing edge (which is dropped).
		seen := map[uuid.UUID]bool{rootSkill.ID: true}
		var attach func(parent *models.SkillTreeNode, parentID uuid.UUID, depth int)
		attach = func(parent *models.SkillTreeNode, parentID uuid.UUID, depth int) {
			for _, childID := range childIDs[parentID] {
				if seen[childID] {
					continue
				}
				fn, ok := nodeMap[childID]
				if !ok {
					continue
				}
				seen[childID] = true
				parent.Children = append(parent.Children, models.SkillTreeNode{
					Skill:    fn.skill,
					Depth:    depth,
					Children: []models.SkillTreeNode{},
				})
				attach(&parent.Children[len(parent.Children)-1], childID, depth+1)
			}
		}
		attach(root, rootSkill.ID, 1)
	}

	return root, nil
}

// flatNode is a flattened dependency-tree node carrying the skill, its depth
// in the tree, and the relation type that connected it to its parent.
type flatNode struct {
	skill        models.Skill
	depth        int
	relationType models.DependencyType
}

// collectIDs extracts skill IDs from flat nodes for the parent query.
func collectIDs(nodes []flatNode) []uuid.UUID {
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.skill.ID
	}
	return ids
}

// GetDependents returns all skills that directly depend on the given skill.
func (s *Store) GetDependents(ctx context.Context, skillID uuid.UUID) ([]models.Skill, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.name, s.version, s.title, s.description, s.content, s.metadata, s.status, s.kind, s.created_at, s.updated_at
		FROM skill_dependencies sd
		JOIN skills s ON s.id = sd.skill_id
		WHERE sd.depends_on = $1
		ORDER BY s.name
	`, skillID)
	if err != nil {
		return nil, fmt.Errorf("query dependents: %w", err)
	}
	defer rows.Close()

	return scanSkills(rows)
}

// GetAllDependencies returns a flat list of all transitive dependencies for a skill.
// Depth is capped at 50 levels to prevent runaway queries on large/cyclic graphs.
func (s *Store) GetAllDependencies(ctx context.Context, skillID uuid.UUID) ([]models.Skill, error) {
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE all_deps AS (
			SELECT depends_on AS id, 1 AS depth
			FROM skill_dependencies
			WHERE skill_id = $1

			UNION

			SELECT sd.depends_on, ad.depth + 1
			FROM skill_dependencies sd
			INNER JOIN all_deps ad ON ad.id = sd.skill_id
			WHERE ad.depth < 50
		)
		SELECT DISTINCT s.id, s.name, s.version, s.title, s.description, s.content, s.metadata, s.status, s.kind, s.created_at, s.updated_at
		FROM all_deps ad
		JOIN skills s ON s.id = ad.id
		ORDER BY s.name
	`, skillID)
	if err != nil {
		return nil, fmt.Errorf("recursive all-deps query: %w", err)
	}
	defer rows.Close()

	return scanSkills(rows)
}

// scanSkills is a helper to scan rows into a slice of Skill.
func scanSkills(rows pgx.Rows) ([]models.Skill, error) {
	var skills []models.Skill
	for rows.Next() {
		var sk models.Skill
		var metaJSON []byte
		if err := rows.Scan(
			&sk.ID, &sk.Name, &sk.Version, &sk.Title,
			&sk.Description, &sk.Content, &metaJSON,
			&sk.Status, &sk.Kind, &sk.CreatedAt, &sk.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		sk.Metadata = metaJSON
		skills = append(skills, sk)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}

	return skills, nil
}

// ---------------------------------------------------------------------------
// Registry update helpers (called from graph mutations)
// ---------------------------------------------------------------------------

// UpdateRegistryAfterChange recalculates registry state for affected skills.
// Should be called after any skill or dependency mutation.
func (s *Store) UpdateRegistryAfterChange(ctx context.Context, skillID uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Recalculate missing deps for this skill
		if err := s.recalcMissingDeps(ctx, tx, skillID); err != nil {
			return err
		}

		// Recalculate missing deps for all skills that depend on this one
		_, err := tx.Exec(ctx, `
			UPDATE skill_registry sr
			SET missing_deps = COALESCE((
				SELECT array_agg(s2.name ORDER BY s2.name)
				FROM skill_dependencies sd
				JOIN skills s2 ON s2.id = sd.depends_on
				WHERE sd.skill_id = sr.skill_id
				  AND NOT EXISTS (
					  SELECT 1 FROM skills s3
					  WHERE s3.id = sd.depends_on
					  AND s3.status IN ('validated', 'active')
				  )
			), '{}')
			WHERE sr.skill_id IN (
				SELECT skill_id FROM skill_dependencies WHERE depends_on = $1
			)
		`, skillID)

		return err
	})
}

// logAuditGraph is a convenience wrapper for graph-related audit events.
func (s *Store) logAuditGraph(ctx context.Context, tx pgx.Tx, event string, skillID uuid.UUID, details map[string]interface{}) error {
	return s.logAudit(ctx, tx, event, &skillID, details)
}

// logAuditEvent logs a generic audit event without a specific skill ID.
func (s *Store) logAuditEvent(ctx context.Context, tx pgx.Tx, event string, details map[string]interface{}) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (ts, event, skill_id, details)
		VALUES ($1, $2, NULL, $3)
	`, time.Now().UTC(), event, detailsJSON)

	return err
}
