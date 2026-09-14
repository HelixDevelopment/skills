package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Vector search
// ---------------------------------------------------------------------------

// VectorSearchResult holds a single result from vector similarity search.
type VectorSearchResult struct {
	ID    uuid.UUID
	Score float64 // cosine similarity (higher = more similar)
}

// VectorSearch performs cosine-similarity search against the embedding
// column of the given table. Returns up to limit results ordered by
// similarity descending.
//
// The table must have an embedding column of type vector(N) and an
// HNSW index for acceptable performance at scale.
func VectorSearch(
	ctx context.Context,
	pool *Pool,
	table string,
	embedding pgvector.Vector,
	limit int,
) ([]VectorSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Validate table name to prevent injection.
	sanitized, err := validateTableName(table)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		`SELECT id, 1 - (embedding <=> $1) AS score
		 FROM %s
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1
		 LIMIT $2`,
		sanitized,
	)

	rows, err := pool.Query(ctx, query, embedding, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search on %s: %w", sanitized, err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Score); err != nil {
			return nil, fmt.Errorf("scan vector search result: %w", err)
		}
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search rows iteration: %w", err)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Hybrid search (RRF)
// ---------------------------------------------------------------------------

// HybridSearch combines vector similarity and full-text keyword search
// using Reciprocal Rank Fusion (RRF). The final results are ordered by
// the RRF score descending.
//
// The keywords string is split on whitespace and used in a tsvector
// match against the name, title, and description columns.
func HybridSearch(
	ctx context.Context,
	pool *Pool,
	table string,
	embedding pgvector.Vector,
	keywords string,
	limit int,
) ([]VectorSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	sanitized, err := validateTableName(table)
	if err != nil {
		return nil, err
	}

	// Build tsquery from keywords.
	tsquery := buildTsQuery(keywords)
	if tsquery == "" {
		// No valid keywords — fall back to pure vector search.
		return VectorSearch(ctx, pool, table, embedding, limit)
	}

	const k = 60.0 // RRF constant; higher = smoother ranking

	query := fmt.Sprintf(
		`WITH
		 vector_ranks AS (
			 SELECT id,
					ROW_NUMBER() OVER (ORDER BY embedding <=> $1) AS rank
			 FROM %s
			 WHERE embedding IS NOT NULL
		 ),
		 text_ranks AS (
			 SELECT id,
					ROW_NUMBER() OVER (
						ORDER BY ts_rank_cd(
							setweight(to_tsvector('english', COALESCE(name, '')), 'A') ||
							setweight(to_tsvector('english', COALESCE(title, '')), 'B') ||
							setweight(to_tsvector('english', COALESCE(description, '')), 'C'),
							to_tsquery('english', $3)
						) DESC
					) AS rank
			 FROM %s
			 WHERE to_tsvector('english',
					COALESCE(name, '') || ' ' ||
					COALESCE(title, '') || ' ' ||
					COALESCE(description, '')) @@ to_tsquery('english', $3)
		 )
		 SELECT COALESCE(v.id, t.id) AS id,
				COALESCE(1.0 / (%[3]f + v.rank), 0.0) +
				COALESCE(1.0 / (%[3]f + t.rank), 0.0) AS score
		 FROM vector_ranks v
		 FULL OUTER JOIN text_ranks t ON v.id = t.id
		 ORDER BY score DESC
		 LIMIT $2`,
		sanitized,
		sanitized,
		k,
	)

	rows, err := pool.Query(ctx, query, embedding, limit, tsquery)
	if err != nil {
		return nil, fmt.Errorf("hybrid search on %s: %w", sanitized, err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Score); err != nil {
			return nil, fmt.Errorf("scan hybrid search result: %w", err)
		}
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hybrid search rows iteration: %w", err)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Embedding storage helpers
// ---------------------------------------------------------------------------

// StoreEmbedding updates the embedding vector for a specific skill.
func StoreSkillEmbedding(
	ctx context.Context,
	pool *Pool,
	skillID uuid.UUID,
	embedding pgvector.Vector,
) error {
	const query = `UPDATE skills SET embedding = $1 WHERE id = $2`
	if _, err := pool.Exec(ctx, query, embedding, skillID); err != nil {
		return fmt.Errorf("store embedding for skill %s: %w", skillID, err)
	}
	return nil
}

// ClearSkillEmbedding sets a skill's embedding column to NULL. This is the
// honest-degrade counterpart to StoreSkillEmbedding, used by
// internal/skill.Store's embedWriteThrough (§G59 F3, code-review remediation)
// on ALL FOUR of its embed failure/skip paths -- empty embed text, an
// Embed() error, Embed() returning an empty/unusable vector, and (F3 round-2
// remediation) Embed() succeeding but the subsequent StoreSkillEmbedding call
// itself failing (e.g. a dimension mismatch between the returned vector and
// this table's `embedding vector(768)` column) -- so a re-embed attempted
// during an ON CONFLICT (name) DO UPDATE never leaves a STALE vector -- one
// computed from the skill's PREVIOUS content -- silently serving vector-KNN
// matches against content that no longer exists. "A failed store means the
// clear would fail too" does NOT generally hold: this UPDATE writes a literal
// NULL, which has no dimension (or other content-shaped constraint) to
// violate, so it succeeds independently of why the preceding store failed.
// Called unconditionally on every one of those four paths regardless of
// whether the skill was freshly inserted or updated: on a fresh insert the
// column is already NULL by column default, so this is a harmless no-op
// there.
func ClearSkillEmbedding(
	ctx context.Context,
	pool *Pool,
	skillID uuid.UUID,
) error {
	const query = `UPDATE skills SET embedding = NULL WHERE id = $1`
	if _, err := pool.Exec(ctx, query, skillID); err != nil {
		return fmt.Errorf("clear embedding for skill %s: %w", skillID, err)
	}
	return nil
}

// StoreEvidenceEmbedding updates the embedding vector for a specific evidence record.
func StoreEvidenceEmbedding(
	ctx context.Context,
	pool *Pool,
	evidenceID uuid.UUID,
	embedding pgvector.Vector,
) error {
	const query = `UPDATE evidences SET embedding = $1 WHERE id = $2`
	if _, err := pool.Exec(ctx, query, embedding, evidenceID); err != nil {
		return fmt.Errorf("store embedding for evidence %s: %w", evidenceID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Find similar skills by text (convenience)
// ---------------------------------------------------------------------------

// FindSimilarSkills performs vector search against the skills table and
// returns the matching skill IDs with scores.
func FindSimilarSkills(
	ctx context.Context,
	pool *Pool,
	embedding pgvector.Vector,
	limit int,
) ([]VectorSearchResult, error) {
	return VectorSearch(ctx, pool, "skills", embedding, limit)
}

// FindSimilarEvidences performs vector search against the evidences table.
func FindSimilarEvidences(
	ctx context.Context,
	pool *Pool,
	embedding pgvector.Vector,
	limit int,
) ([]VectorSearchResult, error) {
	return VectorSearch(ctx, pool, "evidences", embedding, limit)
}

// ---------------------------------------------------------------------------
// Vector search with filters
// ---------------------------------------------------------------------------

// VectorSearchFiltered performs vector search with an additional status filter.
func VectorSearchFiltered(
	ctx context.Context,
	pool *Pool,
	table string,
	embedding pgvector.Vector,
	status string,
	limit int,
) ([]VectorSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	sanitized, err := validateTableName(table)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		`SELECT id, 1 - (embedding <=> $1) AS score
		 FROM %s
		 WHERE embedding IS NOT NULL AND status = $3
		 ORDER BY embedding <=> $1
		 LIMIT $2`,
		sanitized,
	)

	rows, err := pool.Query(ctx, query, embedding, limit, status)
	if err != nil {
		return nil, fmt.Errorf("filtered vector search on %s: %w", sanitized, err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Score); err != nil {
			return nil, fmt.Errorf("scan filtered vector search result: %w", err)
		}
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filtered vector search rows iteration: %w", err)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// tableNamePattern is the strict allowlist for a SQL table identifier that may
// be interpolated into a query string. A valid name starts with a letter or
// underscore and thereafter contains only letters, digits, and underscores.
// Leading digits, whitespace, punctuation, and any SQL metacharacter are all
// rejected.
var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxTableNameLen bounds identifier length. PostgreSQL truncates identifiers at
// 63 bytes (NAMEDATALEN-1); anything longer is a programmer error, not a table.
const maxTableNameLen = 63

// validateTableName rejects any table name that is not a strict SQL identifier,
// returning an error instead of a mutated name. Unlike the previous strip-based
// sanitiser (which turned e.g. "skills; DROP" into the wrong-but-valid
// "skillsDROP" and let it through), this NEVER alters the input: a
// non-conforming name — empty, over-length, leading-digit, or containing any
// character outside [A-Za-z0-9_] — yields a non-nil error. Callers MUST
// propagate the error and never proceed on a rejected name (§11.4.201: the
// guard asserts the real condition and refuses on violation rather than
// transforming bad input into passable input).
func validateTableName(name string) (string, error) {
	if len(name) > maxTableNameLen {
		return "", fmt.Errorf("invalid table name %q: exceeds %d bytes", name, maxTableNameLen)
	}
	if !tableNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid table name: %q", name)
	}
	return name, nil
}

// buildTsQuery converts a space-separated keyword string into a PostgreSQL
// tsquery expression with AND semantics.
func buildTsQuery(keywords string) string {
	words := strings.Fields(keywords)
	if len(words) == 0 {
		return ""
	}
	// Escape each word and join with & (AND).
	escaped := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		// Remove any characters that would break tsquery syntax.
		w = sanitizeTsQueryWord(w)
		if w != "" {
			escaped = append(escaped, w+":*") // prefix matching
		}
	}
	if len(escaped) == 0 {
		return ""
	}
	return strings.Join(escaped, " & ")
}

// sanitizeTsQueryWord removes characters that would break a tsquery.
func sanitizeTsQueryWord(w string) string {
	var b strings.Builder
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Vector stats / diagnostics
// ---------------------------------------------------------------------------

// VectorIndexStats returns the number of indexed vectors and index size
// for the given table's embedding column.
func VectorIndexStats(
	ctx context.Context,
	pool *Pool,
	table string,
) (indexedCount int64, indexSizeBytes int64, err error) {
	sanitized, err := validateTableName(table)
	if err != nil {
		return 0, 0, err
	}

	// Count non-null embeddings.
	countQuery := fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE embedding IS NOT NULL`, sanitized)
	if err := pool.QueryRow(ctx, countQuery).Scan(&indexedCount); err != nil {
		return 0, 0, fmt.Errorf("count vectors in %s: %w", sanitized, err)
	}

	// Get index size for the HNSW index (best-effort; may not exist).
	indexName := "idx_" + sanitized + "_embedding"
	var sizeMB float64
	err = pool.QueryRow(ctx,
		`SELECT pg_size_pretty(pg_relation_size($1)), pg_relation_size($1)`,
		indexName,
	).Scan(new(string), &indexSizeBytes)
	if err != nil {
		// Index may not exist yet; that's OK.
		indexSizeBytes = 0
	}
	_ = sizeMB

	return indexedCount, indexSizeBytes, nil
}

// indexReadyQuery reports whether an index (identified by its pg_class.relname)
// is valid. The index name lives on pg_class, NOT on pg_index — pg_index has no
// name column, only indexrelid, an OID reference into pg_class. The earlier
// `SELECT indisvalid FROM pg_index WHERE indexrelname = $1` form therefore
// always failed at parse/plan time with `column "indexrelname" does not exist`,
// and because that error was swallowed by a catch-all "not ready yet" branch,
// WaitForVectorIndexReady could only ever time out — it never detected a ready
// index. Joining pg_index to pg_class on indexrelid and matching c.relname is
// the correct catalog lookup. A genuinely absent index returns ZERO rows
// (pgx.ErrNoRows), which is the real "still building / not created yet" signal.
const indexReadyQuery = `SELECT i.indisvalid
	FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	WHERE c.relname = $1`

// WaitForVectorIndexReady polls until the pgvector HNSW index for the
// given table is ready (indisvalid = true), or until the timeout.
//
// Error handling distinguishes the two cases the previous swallow-everything
// `continue` conflated (§11.4.201 — a guard must assert the real condition, not
// a proxy that any failure satisfies): pgx.ErrNoRows means the index does not
// exist yet and polling should continue, whereas any OTHER query error means
// the query itself (or the connection) is broken and is surfaced immediately
// rather than silently spun on until the deadline. A per-iteration query
// cancelled by this function's own deadline is not a broken query — it is
// deferred to the ctx.Done() branch so the timeout is reported once, with its
// clearer message.
func WaitForVectorIndexReady(
	ctx context.Context,
	pool *Pool,
	table string,
	timeout time.Duration,
) error {
	sanitized, err := validateTableName(table)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	indexName := "idx_" + sanitized + "_embedding"

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for index %s to be ready: %w", indexName, ctx.Err())
		case <-ticker.C:
			var isValid bool
			err := pool.QueryRow(ctx, indexReadyQuery, indexName).Scan(&isValid)
			switch {
			case err == nil:
				if isValid {
					return nil
				}
				zap.L().Debug("vector index still building", zap.String("index", indexName))
			case errors.Is(err, pgx.ErrNoRows):
				// Index does not exist yet — the genuine "keep waiting" case.
				zap.L().Debug("vector index not found yet", zap.String("index", indexName))
			case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				// The per-iteration query lost the race with the deadline; let
				// the ctx.Done() branch report the timeout on the next loop turn.
				zap.L().Debug("vector index readiness query cancelled", zap.String("index", indexName))
			default:
				// A genuine query/connection error (broken SQL, dead pool) —
				// surface it rather than spinning until timeout, which is exactly
				// the failure the previous catch-all `continue` masked.
				return fmt.Errorf("query readiness of index %s: %w", indexName, err)
			}
		}
	}
}
