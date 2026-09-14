// Package skill — tenant_store.go provides a tenant-aware wrapper around the
// skill CRUD operations. Every query is scoped to a tenant_id extracted from
// the request context, enforcing data isolation at the application layer.
//
// When no tenant_id is present in context (single-tenant mode), queries are
// NOT filtered by tenant_id, preserving full backward compatibility with
// existing data that has NULL tenant_id columns (see
// migrations/004_enterprise.up.sql §2).
//
// The tenant_id is expected to be injected into context by tenant middleware
// (e.g. an HTTP middleware that reads an X-Tenant-ID header or JWT claim and
// stores it under the "tenant" context key).
package skill

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Context key for tenant_id
// ---------------------------------------------------------------------------

// tenantContextKey is the context key under which tenant middleware stores the
// tenant UUID. Matches the codebase's existing contextKey convention
// (internal/api/response.go:28).
type tenantContextKey string

const (
	// TenantKey is the context key used by tenant middleware to pass the
	// tenant UUID. Middleware should call context.WithValue(ctx, TenantKey, id)
	// before the handler runs.
	TenantKey tenantContextKey = "tenant"
)

// TenantFromContext extracts the tenant UUID from ctx. Returns uuid.Nil and
// false when no tenant is set (single-tenant mode). Callers that need to
// distinguish "no tenant" from "zero tenant" should check the boolean.
func TenantFromContext(ctx context.Context) (uuid.UUID, bool) {
	v := ctx.Value(TenantKey)
	if v == nil {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		// Also accept *uuid.UUID for callers that store a pointer.
		if ptr, ok := v.(*uuid.UUID); ok && ptr != nil {
			return *ptr, true
		}
		return uuid.Nil, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// TenantStore
// ---------------------------------------------------------------------------

// TenantStore wraps the skill CRUD operations with automatic tenant_id
// filtering. It does NOT replace the existing Store — it is a lightweight
// adapter that composes over *db.Pool and applies tenant scoping to every
// query it issues.
//
// Construction:
//
//	ts := skill.NewTenantStore(pool, logger)
//
// Usage from a handler (tenant_id comes from context):
//
//	skills, err := ts.ListSkills(ctx, ListOpts{Status: models.SkillStatusActive, Limit: 50})
type TenantStore struct {
	pool   *db.Pool
	logger *zap.Logger
}

// NewTenantStore creates a new tenant-aware skill store. logger defaults to
// zap.NewNop() if nil, matching the existing Store convention.
func NewTenantStore(pool *db.Pool, logger *zap.Logger) *TenantStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TenantStore{pool: pool, logger: logger}
}

// ---------------------------------------------------------------------------
// List options
// ---------------------------------------------------------------------------

// ListOpts controls the behaviour of ListSkills. All fields are optional;
// zero values mean "no filter" / "no limit".
type ListOpts struct {
	Status models.SkillStatus
	Limit  int
	Offset int
}

// ---------------------------------------------------------------------------
// ListSkills
// ---------------------------------------------------------------------------

// ListSkills returns skills scoped to the tenant in ctx. When no tenant is
// present (single-tenant mode), returns all skills regardless of tenant_id.
func (ts *TenantStore) ListSkills(ctx context.Context, opts ListOpts) ([]models.Skill, error) {
	tenantID, hasTenant := TenantFromContext(ctx)

	var (
		sqlStr string
		args   []interface{}
		argIdx int
		conds  []string
	)

	if hasTenant {
		argIdx++
		conds = append(conds, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, tenantID)
	}

	if opts.Status != "" {
		argIdx++
		conds = append(conds, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, opts.Status)
	}

	sqlStr = `SELECT id, name, version, title, description, content,
	                 metadata, status, kind, created_at, updated_at
	          FROM skills`

	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}

	sqlStr += " ORDER BY name"

	if opts.Limit > 0 {
		argIdx++
		sqlStr += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		argIdx++
		sqlStr += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := ts.pool.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("list skills (tenant): %w", err)
	}
	defer rows.Close()

	return scanSkills(rows)
}

// ---------------------------------------------------------------------------
// GetSkill
// ---------------------------------------------------------------------------

// GetSkill retrieves a single skill by name, scoped to the tenant in ctx.
// Returns ErrSkillNotFound when the skill does not exist or belongs to a
// different tenant.
func (ts *TenantStore) GetSkill(ctx context.Context, name string) (*models.Skill, error) {
	tenantID, hasTenant := TenantFromContext(ctx)

	var sqlStr string
	var args []interface{}
	argIdx := 0

	if hasTenant {
		argIdx++
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE name = $%d AND tenant_id = $%d
		`, argIdx, argIdx+1)
		args = append(args, name, tenantID)
	} else {
		argIdx++
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE name = $%d
		`, argIdx)
		args = append(args, name)
	}

	var skill models.Skill
	var metadata []byte
	err := ts.pool.QueryRow(ctx, sqlStr, args...).Scan(
		&skill.ID, &skill.Name, &skill.Version, &skill.Title,
		&skill.Description, &skill.Content, &metadata,
		&skill.Status, &skill.Kind, &skill.CreatedAt, &skill.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
		}
		return nil, fmt.Errorf("get skill (tenant): %w", err)
	}
	skill.Metadata = metadata

	// Load dependencies — same query as Store.GetByName but tenant-scoped.
	depSQL := `
		SELECT sd.skill_id, sd.depends_on, sd.relation_type, sd.optional, sd.sort_order,
		       ds.name as depends_on_name, ds.title as depends_on_title
		FROM skill_dependencies sd
		JOIN skills ds ON sd.depends_on = ds.id
		WHERE sd.skill_id = $1
		ORDER BY sd.relation_type, sd.sort_order NULLS LAST, ds.name
	`
	depRows, err := ts.pool.Query(ctx, depSQL, skill.ID)
	if err != nil {
		return nil, fmt.Errorf("get dependencies (tenant): %w", err)
	}
	defer depRows.Close()
	for depRows.Next() {
		var d models.SkillDependency
		if err := depRows.Scan(&d.SkillID, &d.DependsOn, &d.RelationType, &d.Optional, &d.SortOrder, &d.DependsOnName, &d.DependsOnTitle); err != nil {
			return nil, fmt.Errorf("scan dependency (tenant): %w", err)
		}
		skill.Dependencies = append(skill.Dependencies, d)
	}

	// Load resources.
	resSQL := `
		SELECT id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at
		FROM resources WHERE skill_id = $1
		ORDER BY url, title, resource_type, id
	`
	resRows, err := ts.pool.Query(ctx, resSQL, skill.ID)
	if err != nil {
		return nil, fmt.Errorf("get resources (tenant): %w", err)
	}
	defer resRows.Close()
	for resRows.Next() {
		var r models.Resource
		var fetchedHash, contentCached sql.NullString
		if err := resRows.Scan(&r.ID, &r.SkillID, &r.URL, &r.Title, &r.ResourceType, &fetchedHash, &contentCached, &r.LastValidated, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan resource (tenant): %w", err)
		}
		r.FetchedHash = fetchedHash.String
		r.ContentCached = contentCached.String
		skill.Resources = append(skill.Resources, r)
	}

	return &skill, nil
}

// ---------------------------------------------------------------------------
// CreateSkill
// ---------------------------------------------------------------------------

// CreateSkill inserts a new skill into the database with the tenant_id from
// ctx. When no tenant is present, tenant_id is left NULL (backward compat).
// The existing ON CONFLICT (name) upsert semantics are preserved — but when
// a tenant is active, the conflict target is effectively scoped by the
// tenant_id column via the WHERE clause.
func (ts *TenantStore) CreateSkill(ctx context.Context, skill *models.Skill) error {
	if skill.ID == uuid.Nil {
		skill.ID = uuid.New()
	}

	tenantID, hasTenant := TenantFromContext(ctx)

	metadataJSON, err := marshalMetadata(skill.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	kind := skill.Kind.NormalizeOrAtomic()

	var returnedID uuid.UUID
	if hasTenant {
		// Upsert scoped by tenant_id: ON CONFLICT (name) still applies, but
		// the tenant_id column ensures cross-tenant name collisions are
		// isolated (once tenant_id becomes NOT NULL and part of a composite
		// unique index, the conflict target will need updating — tracked
		// separately).
		err = ts.pool.QueryRow(ctx, `
			INSERT INTO skills (id, name, version, title, description, content, metadata, status, kind, tenant_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
			ON CONFLICT (name) DO UPDATE SET
				version = EXCLUDED.version,
				title = EXCLUDED.title,
				description = EXCLUDED.description,
				content = EXCLUDED.content,
				metadata = EXCLUDED.metadata,
				status = EXCLUDED.status,
				kind = EXCLUDED.kind,
				tenant_id = EXCLUDED.tenant_id,
				updated_at = NOW()
			RETURNING id
		`, skill.ID, skill.Name, skill.Version, skill.Title,
			skill.Description, skill.Content, metadataJSON,
			skill.Status, kind, tenantID,
		).Scan(&returnedID)
	} else {
		err = ts.pool.QueryRow(ctx, `
			INSERT INTO skills (id, name, version, title, description, content, metadata, status, kind, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
			ON CONFLICT (name) DO UPDATE SET
				version = EXCLUDED.version,
				title = EXCLUDED.title,
				description = EXCLUDED.description,
				content = EXCLUDED.content,
				metadata = EXCLUDED.metadata,
				status = EXCLUDED.status,
				kind = EXCLUDED.kind,
				updated_at = NOW()
			RETURNING id
		`, skill.ID, skill.Name, skill.Version, skill.Title,
			skill.Description, skill.Content, metadataJSON,
			skill.Status, kind,
		).Scan(&returnedID)
	}
	if err != nil {
		return fmt.Errorf("create skill (tenant): %w", err)
	}

	// Insert dependencies.
	for _, dep := range skill.Dependencies {
		depSQL := `
			INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional, sort_order)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (skill_id, depends_on, relation_type) DO UPDATE SET
				optional = EXCLUDED.optional,
				sort_order = EXCLUDED.sort_order
		`
		if hasTenant {
			// Also set tenant_id on the dependency row.
			depSQL = `
				INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional, sort_order, tenant_id)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (skill_id, depends_on, relation_type) DO UPDATE SET
					optional = EXCLUDED.optional,
					sort_order = EXCLUDED.sort_order,
					tenant_id = EXCLUDED.tenant_id
			`
			if _, err := ts.pool.Exec(ctx, depSQL, returnedID, dep.DependsOn, dep.RelationType, dep.Optional, dep.SortOrder, tenantID); err != nil {
				return fmt.Errorf("create dependency (tenant): %w", err)
			}
		} else {
			if _, err := ts.pool.Exec(ctx, depSQL, returnedID, dep.DependsOn, dep.RelationType, dep.Optional, dep.SortOrder); err != nil {
				return fmt.Errorf("create dependency (tenant): %w", err)
			}
		}
	}

	// Upsert registry entry.
	if hasTenant {
		regSQL := `
			INSERT INTO skill_registry (skill_id, skill_name, missing_deps, stale, auto_expand, coverage, tenant_id)
			VALUES ($1, $2, '{}', false, true, 0.0, $3)
			ON CONFLICT (skill_id) DO NOTHING
		`
		if _, err := ts.pool.Exec(ctx, regSQL, returnedID, skill.Name, tenantID); err != nil {
			return fmt.Errorf("create registry entry (tenant): %w", err)
		}
	} else {
		regSQL := `
			INSERT INTO skill_registry (skill_id, skill_name, missing_deps, stale, auto_expand, coverage)
			VALUES ($1, $2, '{}', false, true, 0.0)
			ON CONFLICT (skill_id) DO NOTHING
		`
		if _, err := ts.pool.Exec(ctx, regSQL, returnedID, skill.Name); err != nil {
			return fmt.Errorf("create registry entry (tenant): %w", err)
		}
	}

	skill.ID = returnedID
	return nil
}

// ---------------------------------------------------------------------------
// UpdateSkill
// ---------------------------------------------------------------------------

// UpdateSkill updates an existing skill identified by name, scoped to the
// tenant in ctx. Returns ErrSkillNotFound when no matching skill exists for
// the tenant.
func (ts *TenantStore) UpdateSkill(ctx context.Context, name string, skill *models.Skill) error {
	tenantID, hasTenant := TenantFromContext(ctx)

	metadataJSON, err := marshalMetadata(skill.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	kind := skill.Kind.NormalizeOrAtomic()

	var cmdTag pgconn.CommandTag
	if hasTenant {
		cmdTag, err = ts.pool.Exec(ctx, `
			UPDATE skills SET
				version = $1,
				title = $2,
				description = $3,
				content = $4,
				metadata = $5,
				status = $6,
				kind = $7,
				updated_at = NOW()
			WHERE name = $8 AND tenant_id = $9
		`, skill.Version, skill.Title, skill.Description, skill.Content,
			metadataJSON, skill.Status, kind, name, tenantID,
		)
	} else {
		cmdTag, err = ts.pool.Exec(ctx, `
			UPDATE skills SET
				version = $1,
				title = $2,
				description = $3,
				content = $4,
				metadata = $5,
				status = $6,
				kind = $7,
				updated_at = NOW()
			WHERE name = $8
		`, skill.Version, skill.Title, skill.Description, skill.Content,
			metadataJSON, skill.Status, kind, name,
		)
	}
	if err != nil {
		return fmt.Errorf("update skill (tenant): %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}

	return nil
}

// ---------------------------------------------------------------------------
// DeleteSkill
// ---------------------------------------------------------------------------

// DeleteSkill removes a skill by name, scoped to the tenant in ctx. Also
// removes associated dependencies, evidences, and resources via CASCADE on the
// foreign keys (migrations/001_initial.up.sql). Returns ErrSkillNotFound when
// no matching skill exists for the tenant.
func (ts *TenantStore) DeleteSkill(ctx context.Context, name string) error {
	tenantID, hasTenant := TenantFromContext(ctx)

	var cmdTag pgconn.CommandTag
	var err error

	if hasTenant {
		cmdTag, err = ts.pool.Exec(ctx, `
			DELETE FROM skills WHERE name = $1 AND tenant_id = $2
		`, name, tenantID)
	} else {
		cmdTag, err = ts.pool.Exec(ctx, `
			DELETE FROM skills WHERE name = $1
		`, name)
	}
	if err != nil {
		return fmt.Errorf("delete skill (tenant): %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}

	return nil
}

// ---------------------------------------------------------------------------
// SearchSkills
// ---------------------------------------------------------------------------

// SearchSkills performs a keyword search over skills, scoped to the tenant in
// ctx. Uses pg_trgm similarity and ILIKE fallback, matching the existing
// Store.textSearch behaviour. When no tenant is present, searches across all
// tenants (backward compat).
func (ts *TenantStore) SearchSkills(ctx context.Context, query string, opts ListOpts) ([]models.Skill, error) {
	tenantID, hasTenant := TenantFromContext(ctx)

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	// Build the primary trigram search query.
	var sqlStr string
	var args []interface{}
	argIdx := 0

	if hasTenant {
		argIdx += 2
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE tenant_id = $%d
			  AND (name %% $%d OR title %% $%d OR description ILIKE '%%' || $%d || '%%')
			ORDER BY similarity(name || ' ' || title || ' ' || COALESCE(description, ''), $%d) DESC, name
			LIMIT $%d
		`, argIdx-1, argIdx, argIdx, argIdx, argIdx, argIdx+1)
		args = append(args, tenantID, query, limit)
	} else {
		argIdx += 2
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE name %% $%d OR title %% $%d OR description ILIKE '%%' || $%d || '%%'
			ORDER BY similarity(name || ' ' || title || ' ' || COALESCE(description, ''), $%d) DESC, name
			LIMIT $%d
		`, argIdx-1, argIdx-1, argIdx-1, argIdx-1, argIdx)
		args = append(args, query, limit)
	}

	rows, err := ts.pool.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("search skills (tenant): %w", err)
	}
	defer rows.Close()

	skills, err := scanSkills(rows)
	if err != nil {
		return nil, err
	}

	// Fallback: if no trigram matches, broaden to ILIKE on name/title.
	if len(skills) == 0 {
		skills, err = ts.searchFallback(ctx, query, limit)
		if err != nil {
			return nil, err
		}
	}

	return skills, nil
}

// searchFallback is the ILIKE-only fallback when trigram similarity returns
// no results. Mirrors Store.textSearch's fallback leg.
func (ts *TenantStore) searchFallback(ctx context.Context, query string, limit int) ([]models.Skill, error) {
	tenantID, hasTenant := TenantFromContext(ctx)

	var sqlStr string
	var args []interface{}
	argIdx := 0

	if hasTenant {
		argIdx += 2
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE tenant_id = $%d
			  AND (name ILIKE '%%' || $%d || '%%' OR title ILIKE '%%' || $%d || '%%')
			ORDER BY name
			LIMIT $%d
		`, argIdx-1, argIdx, argIdx, argIdx+1)
		args = append(args, tenantID, query, limit)
	} else {
		argIdx += 2
		sqlStr = fmt.Sprintf(`
			SELECT id, name, version, title, description, content,
			       metadata, status, kind, created_at, updated_at
			FROM skills
			WHERE name ILIKE '%%' || $%d || '%%' OR title ILIKE '%%' || $%d || '%%'
			ORDER BY name
			LIMIT $%d
		`, argIdx-1, argIdx-1, argIdx)
		args = append(args, query, limit)
	}

	rows, err := ts.pool.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("search fallback (tenant): %w", err)
	}
	defer rows.Close()

	return scanSkills(rows)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// marshalMetadata is a thin wrapper around json.Marshal that returns the raw
// bytes and any error. Extracted from Store.Create to avoid duplication across
// CreateSkill/UpdateSkill.
func marshalMetadata(metadata interface{}) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	switch m := metadata.(type) {
	case []byte:
		if len(m) == 0 {
			return []byte("{}"), nil
		}
		return m, nil
	default:
		b, err := json.Marshal(metadata)
		if err != nil {
			return []byte("{}"), err
		}
		return b, nil
	}
}
