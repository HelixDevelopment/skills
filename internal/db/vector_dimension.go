package db

// G10 — embedding-dimension boot-time safety assertion
// (research/g10_embedding_provider_design.md §2.2, GAPS_AND_RISKS_REGISTER.md
// §G10). The system stores `vector(N)` columns (migrations/001_initial.up.sql:
// skills.embedding and evidences.embedding, both vector(768)) and configures an
// embedding provider's expected output width (config.EmbeddingConfig.Dimensions,
// surfaced via Embedder.Dimensions()) — but nothing has ever asserted the two
// AGREE. They agree today only by coincidence (both happen to be 768), never by
// construction: no code queries the live database for the column's REAL declared
// dimension and compares it to the configured embedder's declared dimension. A
// single config edit to a differently-sized model produces an opaque pgvector
// insert/query-time error ("expected N dimensions, not M") deep in a background
// worker or API request, far from the edit that caused it.
//
// AssertEmbeddingDimension closes this gap per §11.4.201 (a guard MUST assert
// the REAL condition from the AUTHORITATIVE source, fail-closed on mismatch or
// on an unresolvable signal): it reads the column's ACTUAL dimension from the
// live database's own system catalog — never a hardcoded Go constant, never the
// embedder's self-reported number alone — and refuses to let the caller proceed
// on a mismatch.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// vectorTypePattern matches the exact rendering PostgreSQL's own format_type()
// produces for a dimensioned pgvector column — e.g. "vector(768)" — so the
// authoritative dimension is read from the LIVE catalog's own type-name
// rendering rather than assumed from pgvector's internal atttypmod encoding
// (research/g10_embedding_provider_design.md §5: whether atttypmod stores the
// raw dimension or an offset-encoded value is explicitly left UNCONFIRMED
// pending live verification — format_type's string rendering sidesteps that
// question entirely, since format_type is the same portable, documented
// PostgreSQL catalog function every client (psql \d, pg_dump) relies on to
// render a column's type, regardless of the type's internal typmod encoding).
var vectorTypePattern = regexp.MustCompile(`^vector\((\d+)\)$`)

// QueryColumnVectorDimension reads the ACTUAL pgvector dimension declared on
// table.column from the live database's system catalog (pg_attribute joined to
// pg_class, rendered via format_type) — the single authoritative source of
// truth for "what dimension is this column really sized for" (§11.4.201). It is
// NEVER derived from a hardcoded Go constant or from the embedding provider's
// own configuration.
//
// Returns an error if the column does not exist (or has been dropped), or if it
// exists but is not a dimensioned pgvector column: a bare "vector" with no
// typmod accepts a vector of ANY length, which would defeat the entire purpose
// of a boot-time dimension assertion, so that case is rejected rather than
// silently treated as "no constraint" (§11.4.201: an unresolvable/ambiguous
// signal refuses, it never assumes OK).
func QueryColumnVectorDimension(ctx context.Context, pool *Pool, table, column string) (int, error) {
	sanitizedTable, err := validateTableName(table)
	if err != nil {
		return 0, fmt.Errorf("query column vector dimension: invalid table name: %w", err)
	}
	// Column identifiers follow the exact same SQL-identifier rules as table
	// identifiers; validateTableName's reject-not-strip guard (§11.4.201,
	// §G27) applies unchanged.
	sanitizedColumn, err := validateTableName(column)
	if err != nil {
		return 0, fmt.Errorf("query column vector dimension: invalid column name: %w", err)
	}

	// Resolve the relation via to_regclass($1) -- the SAME unqualified-name
	// resolution PostgreSQL applies to every unqualified table reference the
	// app itself issues (e.g. "FROM skills" elsewhere in this package),
	// honoring search_path exactly as the running application does. A prior
	// version of this query joined on the bare `c.relname = $1` with no
	// schema pin and no relkind filter: with two same-named relations in
	// different schemas (e.g. a decoy backup.skills alongside the real
	// public.skills), that produced an UNORDERED multi-row result and
	// QueryRow.Scan's "take the first row" was plan-dependent and arbitrary
	// -- a real ambiguity, not merely a hypothetical one (§11.4.201: a guard
	// MUST resolve its target the SAME way the system it guards resolves it,
	// never via a second, divergent resolution rule that can silently pick a
	// different relation). to_regclass returns NULL for a name that does not
	// resolve, so a.attrelid = to_regclass($1) matches zero rows in that case
	// (NULL never equals anything) and QueryRow.Scan below still reports
	// pgx.ErrNoRows exactly as it did before -- the "column/table not found"
	// fail-closed path is unchanged. The relkind filter additionally excludes
	// views/sequences/etc., leaving only ordinary and partitioned tables
	// ('r', 'p') as candidates, matching every CREATE TABLE this schema uses.
	const query = `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		WHERE a.attrelid = to_regclass($1)
		  AND a.attname = $2
		  AND NOT a.attisdropped
		  AND c.relkind IN ('r', 'p')`

	var typeStr string
	err = pool.QueryRow(ctx, query, sanitizedTable, sanitizedColumn).Scan(&typeStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("query column vector dimension: column %s.%s not found",
				sanitizedTable, sanitizedColumn)
		}
		return 0, fmt.Errorf("query column vector dimension: %w", err)
	}

	m := vectorTypePattern.FindStringSubmatch(typeStr)
	if m == nil {
		return 0, fmt.Errorf(
			"query column vector dimension: column %s.%s has type %q, expected a dimensioned pgvector column (e.g. vector(768))",
			sanitizedTable, sanitizedColumn, typeStr)
	}
	dim, err := strconv.Atoi(m[1])
	if err != nil {
		// Unreachable in practice (the regex only captures digits), but
		// surfaced rather than ignored per §11.4.6 — never silently assume a
		// parse that could not actually fail.
		return 0, fmt.Errorf("query column vector dimension: parse dimension out of %q: %w", typeStr, err)
	}
	return dim, nil
}

// AssertEmbeddingDimension is the G10 boot-time safety guard (§11.4.201): it
// reads the REAL pgvector dimension declared on table.column from the live
// database (via QueryColumnVectorDimension — the authoritative source) and
// asserts it equals wantDim, the configured embedder's OWN declared/output
// dimension (Embedder.Dimensions()). A mismatch returns a FAIL-CLOSED error
// naming BOTH dimensions and the source of each, so a misconfigured embedder
// can never silently corrupt every StoreSkillEmbedding/StoreEvidenceEmbedding
// insert and every KNN search (VectorSearch/HybridSearch) against a column
// sized for a different provider. Callers (cmd/server, cmd/worker) MUST treat
// a non-nil return as fatal — this is a hard stop, never a warn-and-continue
// (research/g10_embedding_provider_design.md §2.2 point 3).
func AssertEmbeddingDimension(ctx context.Context, pool *Pool, table, column string, wantDim int) error {
	dbDim, err := QueryColumnVectorDimension(ctx, pool, table, column)
	if err != nil {
		return fmt.Errorf("assert embedding dimension for %s.%s: could not resolve column dimension: %w", table, column, err)
	}
	if dbDim != wantDim {
		return fmt.Errorf(
			"embedding dimension mismatch: database column %s.%s is vector(%d) (source: live pg_attribute/format_type catalog lookup) "+
				"but the configured embedder yields dimension %d (source: config.EmbeddingConfig.Dimensions / Embedder.Dimensions()) — "+
				"every StoreSkillEmbedding/StoreEvidenceEmbedding insert and KNN search against %s would be broken; refusing to start",
			table, column, dbDim, wantDim, table)
	}
	return nil
}
