// Package skill provides CRUD and search operations for skills in the knowledge graph.
package skill

import (
	"context"
	// dbsql aliased (not "sql"): nearly every function in this file
	// declares a LOCAL variable literally named `sql` for its query string
	// (e.g. GetByName's `sql := \`SELECT ...\``), which would shadow an
	// unaliased `database/sql` package import within that exact scope --
	// silently making the import unusable (and unused) anywhere it is
	// actually needed.
	dbsql "database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
)

// Sentinel errors returned by the skill store and graph operations. Callers
// should compare against these with errors.Is rather than matching strings.
var (
	// ErrSkillNotFound indicates the requested skill does not exist.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrSkillExists indicates a skill with the same unique name already exists.
	ErrSkillExists = errors.New("skill already exists")
	// ErrInvalidSkill indicates a skill failed structural or semantic validation.
	ErrInvalidSkill = errors.New("invalid skill")
	// ErrDependencyNotFound indicates a referenced dependency skill does not exist.
	ErrDependencyNotFound = errors.New("dependency skill not found")
	// ErrCycleDetected indicates an operation would introduce a dependency cycle.
	ErrCycleDetected = errors.New("dependency cycle detected")
	// NOTE (HXC-159 T-P3.01.5): ErrPartOfUnsupported ("part_of dependency
	// alias not yet supported") is REMOVED — the part_of→inverted-composes
	// inversion is now wired in ImportFromTOML (see the part_of block in
	// import_export.go). Unlike depends_on/prerequisite (a SAME-direction
	// fold to `requires` on the skill being imported), part_of is the child
	// declaring its parent — an INVERTED, CROSS-SKILL edge (parent→child
	// `composes`, per research/skill_granularity_and_composition.md §4.1
	// alias table) — persisted in the same transaction with the PARENT's
	// cycle check; an absent parent hard-errors via ErrDependencyNotFound
	// with no partial persist. The old F1 refusal contract is void by
	// design (§11.4.120 reconciliation, TestG07PartOf_Inverts_NoPartialLoss).
)

// Store provides data access for skills and related entities.
type Store struct {
	pool *db.Pool
	// embedder is the OPTIONAL query-side embedder that upgrades Search from
	// keyword-only to a genuine hybrid (vector KNN + trigram) search (§G29).
	// It is nil by default (NewStore leaves it unset), so every construction
	// path that does not opt in keeps the historical keyword-only behaviour
	// and never makes an embedding call. It is wired in from configuration at
	// MCP-server construction (internal/mcp/server.go) so the live skill_search,
	// REST /search, and pipeline-dedup paths — all sharing this one Store —
	// become hybrid the moment an embedding provider is configured.
	embedder db.Embedder
	// lastEmbedWarnUnixNano is the UnixNano timestamp of the most recent
	// "query embedding failed" warning (§G29 finding 3), throttled via
	// warnEmbeddingDegraded so a sustained embedding-provider outage logs a
	// steady drumbeat of evidence instead of either flooding the log once per
	// query or going completely silent. Accessed only via the atomic type's
	// own methods -- safe for the concurrent Search callers this Store serves.
	lastEmbedWarnUnixNano atomic.Int64
	// lastClearWarnUnixNano is the UnixNano timestamp of the most recent
	// "failed to clear stale embedding" warning (§G59 F6, round-3 Fable-xhigh
	// re-review, LOW finding), throttled via warnEmbeddingClearFailed.
	// Deliberately a SEPARATE counter from lastEmbedWarnUnixNano: two of
	// embedWriteThrough's failure branches call warnEmbeddingDegraded (the
	// embed/store-failure warning) IMMEDIATELY before calling
	// clearStaleEmbedding, so if a clear failure shared lastEmbedWarnUnixNano's
	// throttle window it would ALWAYS lose the CompareAndSwap race against the
	// warning that just fired nanoseconds earlier -- permanently suppressing
	// every clear-failure warning and making the stale-vector-retained
	// condition invisible in logs exactly when an operator needs it most
	// (see warnEmbeddingClearFailed's doc comment for the full account). A
	// distinct counter lets a clear failure log independently of whatever
	// embed/store warning immediately preceded it, while still protecting
	// against a flood of repeated clear failures during a sustained DB outage
	// (throttled at the same embedDegradeWarnInterval cadence).
	lastClearWarnUnixNano atomic.Int64
	// logger is the injected sink for Store's own diagnostics (currently just
	// warnEmbeddingDegraded). Re-review remediation (MAJOR finding, post-G29):
	// warnEmbeddingDegraded originally called the package-level zap.L(), but
	// this codebase's construction path (cmd/server/main.go builds a concrete
	// *zap.Logger via newLogger and threads it explicitly into api.New /
	// mcp.NewMCPServer / validation.NewPipeline -- see internal/api/server.go
	// and internal/mcp/server.go) never calls zap.ReplaceGlobals, so
	// zap.L() resolves to zap's built-in no-op default in every deployed
	// binary. The warning was therefore emitted to a sink nothing reads --
	// dead at the runtime layer despite a green test suite (the pre-fix test
	// only observed the warning by installing its OWN process-global
	// zap.ReplaceGlobals override, which production code never does).
	// Defaults to zap.NewNop() (see NewStore) so every construction path that
	// never calls WithLogger -- every existing test helper, cmd/worker's Store
	// (which never calls Search) -- keeps emitting nothing and never nil-panics,
	// exactly mirroring the historical behaviour for those paths. Wired to a
	// real logger only by internal/mcp.NewMCPServer, mirroring the existing
	// WithEmbedder fluent-option convention immediately below.
	logger *zap.Logger
}

// NewStore creates a new skill store. logger defaults to zap.NewNop() (see
// WithLogger) so a Store used without WithLogger -- every existing test
// helper and any future construction path that does not opt in -- behaves
// exactly as before this field was added: Store's internal diagnostics are
// silently discarded, never a nil-pointer panic.
func NewStore(pool *db.Pool) *Store {
	return &Store{pool: pool, logger: zap.NewNop()}
}

// WithEmbedder configures the query-side embedder that turns Search into a
// genuine hybrid (vector KNN + trigram) search and returns the receiver for
// fluent wiring (§G29). Passing a nil embedder resets Search to keyword-only.
// This is an explicit opt-in: callers that never invoke it (every current test
// helper, and any deployment with no embedding provider configured) keep the
// keyword-only path and never issue an embedding request.
//
// Concurrency contract (Fable code-review remediation, finding 6b): WithEmbedder
// is a plain, unsynchronized field write -- it is SAFE ONLY as a one-time
// construction-time wire-up that happens-before any concurrent Search call, NOT
// as a live runtime reconfiguration switch. The sole production caller,
// internal/mcp.NewMCPServer, relies on exactly this: it calls WithEmbedder on
// the shared *Store it was handed, synchronously, before returning the
// *MCPServer to its caller -- and every transport (stdio/HTTP/ACP) that could
// concurrently invoke Search is started strictly AFTER NewMCPServer returns
// (cmd/server wires the Store, then NewMCPServer, then RegisterTools/
// ListenAndServe/RunStdio/RunACP). There is a SINGLE construction call over the
// Store's lifetime; calling WithEmbedder again after any transport has started
// serving requests is a data race with concurrent Search readers of s.embedder
// and is NOT supported.
func (s *Store) WithEmbedder(e db.Embedder) *Store {
	s.embedder = e
	return s
}

// WithLogger wires the real application logger into Store so its own
// diagnostics (currently just warnEmbeddingDegraded) reach a real sink at
// runtime instead of the package-level zap.L() no-op default (re-review
// remediation, MAJOR finding; see the logger field's doc comment). Mirrors
// the WithEmbedder fluent-option convention above; returns the receiver for
// fluent wiring. A nil logger is a no-op (the field keeps whatever it already
// had -- the zap.NewNop() default from NewStore unless WithLogger was already
// called) rather than falling back to zap.L(), which would silently
// reintroduce the exact dead-sink class this method exists to close.
//
// Concurrency contract: identical to WithEmbedder -- a plain, unsynchronized
// field write, safe ONLY as a one-time construction-time wire-up that
// happens-before any concurrent Search call. internal/mcp.NewMCPServer calls
// WithLogger synchronously, before returning the *MCPServer, alongside its
// existing WithEmbedder call.
func (s *Store) WithLogger(logger *zap.Logger) *Store {
	if logger != nil {
		s.logger = logger
	}
	return s
}

// Pool returns the underlying database pool for operations that need
// direct database access (e.g., audit logging from other packages).
func (s *Store) Pool() *db.Pool {
	return s.pool
}

// rrfK is the Reciprocal Rank Fusion smoothing constant (the field-standard
// default). It damps how quickly a result's contribution decays with rank, so
// no single high-ranked hit in one list can dominate the fused ordering.
const rrfK = 60

// Per-list RRF weights (§G29). Vector recall is weighted marginally above the
// lexical list because the semantic path is the capability G29 restores (R2/R13
// make semantic retrieval core) and this deterministically breaks a rank tie in
// favour of a semantically-near match over an equal-rank purely-lexical one. A
// skill that matches BOTH lexically AND semantically still dominates — it
// accrues both weighted contributions — so exact-name lookups (which land at
// the top of both lists) are unaffected by the slight tilt.
const (
	vectorRRFWeight  = 1.0
	trigramRRFWeight = 0.9
)

// embedDegradeWarnInterval throttles the "query embedding failed, degrading to
// keyword-only" warning (§G29 finding 3) to at most once per this interval per
// Store. A failing embedding provider can degrade EVERY hybrid-search query in
// a hot path; logging every single occurrence would flood the log at exactly
// the moment an operator most needs a readable signal, while suppressing it
// entirely (the pre-fix behaviour) makes an ongoing outage invisible. Once per
// interval keeps a sustained failure OBSERVABLE without spamming.
const embedDegradeWarnInterval = 30 * time.Second

// Search performs a hybrid search over the skill graph.
//
// When a query-side embedder is configured (WithEmbedder) it runs two candidate
// retrievals — a pgvector cosine-KNN over skills.embedding (semantic recall,
// via VectorSearch) and a pg_trgm/ILIKE keyword match (lexical precision) — and
// fuses them with weighted Reciprocal Rank Fusion. Because the fusion is by RANK
// (not by raw score) the incomparable score scales of the two paths cannot
// distort each other, and a semantically-near skill whose text does NOT contain
// the query as a substring can both surface and outrank a purely-lexical match —
// the recall the keyword-only path structurally cannot deliver.
//
// When no embedder is configured, OR the query embedding fails (e.g. an
// embedding provider is temporarily unreachable), Search transparently degrades
// to the keyword-only path rather than returning an error, so keyword search
// keeps working everywhere. A genuine failure of the vector KNN query itself is
// NOT masked — it is returned — because that signals a real misconfiguration
// (e.g. an embedding/column dimension mismatch) that must surface (§11.4.6).
//
// The returned SearchResult.Score is the pg_trgm similarity for the keyword-only
// path, and the fused RRF relevance score for the hybrid path.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]models.SearchResult, error) {
	trigram, err := s.textSearch(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	// No embedder wired ⇒ historical keyword-only behaviour, byte-for-byte.
	if s.embedder == nil {
		return trigram, nil
	}

	vecs, embErr := s.embedder.Embed(ctx, []string{query})
	if embErr != nil {
		// The embedding provider is unavailable for this query: keep search
		// working on the lexical path instead of failing the request -- but,
		// unlike before (§G29 finding 3), make the degradation OBSERVABLE
		// rather than silently swallowing it with zero telemetry.
		s.warnEmbeddingDegraded(embErr)
		return trigram, nil
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		// The provider returned no error but also no usable vector (e.g. an
		// empty batch): degrade quietly, this is not a failure signal worth
		// warning about.
		return trigram, nil
	}

	vector, err := s.VectorSearch(ctx, vecs[0], limit)
	if err != nil {
		// A KNN query error is a real internal fault (e.g. dimension mismatch),
		// not an expected degradation — surface it rather than silently hide it.
		return nil, fmt.Errorf("hybrid search vector leg: %w", err)
	}

	return fuseSearchResults(vector, trigram, limit), nil
}

// warnEmbeddingDegraded logs, at most once per embedDegradeWarnInterval, that
// a query-side embedding call failed and Search degraded to keyword-only
// (§G29 finding 3). Pre-fix, this failure was swallowed with zero telemetry --
// an operator watching logs during a real embedding-provider outage would see
// nothing but degraded search relevance, with no signal pointing at the cause.
// Throttling (rather than logging every occurrence) keeps a sustained outage
// visible without flooding the log once per search request.
//
// Logs via s.logger (injected by WithLogger), NOT the package-level zap.L()
// (re-review remediation, MAJOR finding): this codebase never calls
// zap.ReplaceGlobals, so zap.L() is zap's no-op default in every deployed
// binary -- the pre-fix version of this call was dead at the runtime layer.
// s.logger defaults to zap.NewNop() (see NewStore/WithLogger) so a Store never
// wired with a real logger keeps the historical zero-output behaviour.
func (s *Store) warnEmbeddingDegraded(err error) {
	now := time.Now().UnixNano()
	last := s.lastEmbedWarnUnixNano.Load()
	if now-last < int64(embedDegradeWarnInterval) {
		return
	}
	if !s.lastEmbedWarnUnixNano.CompareAndSwap(last, now) {
		return // another goroutine just logged; avoid a duplicate burst
	}
	s.logger.Warn("hybrid search: query embedding failed, degrading to keyword-only search (§G29)", zap.Error(err))
}

// warnEmbeddingClearFailed logs, at most once per embedDegradeWarnInterval,
// that clearStaleEmbedding's own UPDATE failed to null out a stale embedding
// vector -- meaning the stale vector from a skill's PREVIOUS content is still
// stored and still vector-KNN-servable (§G59 F6, round-3 Fable-xhigh
// re-review, LOW finding).
//
// Uses its OWN throttle counter (lastClearWarnUnixNano), deliberately never
// lastEmbedWarnUnixNano/warnEmbeddingDegraded's: two of embedWriteThrough's
// four failure branches (the Embed-error branch and the
// db.StoreSkillEmbedding-failure branch) call warnEmbeddingDegraded
// IMMEDIATELY before calling clearStaleEmbedding, so a clear failure
// occurring nanoseconds later would -- against the SAME per-Store throttle
// window -- ALWAYS lose the CompareAndSwap race in warnEmbeddingDegraded and
// log NOTHING, permanently suppressing exactly the warning an operator needs
// to see the stale-vector-retained condition. clearStaleEmbedding's doc
// comment previously (pre-round-3) claimed a clear failure is "reported
// through the same throttled warnEmbeddingDegraded sink, never silently
// swallowed"; that claim was FALSE for the sequencing embedWriteThrough's
// branches actually exercise. This method + clearStaleEmbedding's call site
// make the claim true: a clear failure is now visible even immediately after
// a preceding embed/store-failure warning, while a sustained run of repeated
// clear failures (e.g. a full DB outage) is still throttled rather than
// flooding the log.
func (s *Store) warnEmbeddingClearFailed(err error) {
	now := time.Now().UnixNano()
	last := s.lastClearWarnUnixNano.Load()
	if now-last < int64(embedDegradeWarnInterval) {
		return
	}
	if !s.lastClearWarnUnixNano.CompareAndSwap(last, now) {
		return // another goroutine just logged; avoid a duplicate burst
	}
	s.logger.Warn("hybrid search: failed to clear stale embedding after a write-through skip/failure (§G59 F6)", zap.Error(err))
}

// textSearch is the keyword-only leg of Search: a pg_trgm similarity/ILIKE match
// with a broaden-on-empty ILIKE fallback. Its behaviour is identical to the
// pre-§G29 Search so every keyword-only caller is unchanged.
func (s *Store) textSearch(ctx context.Context, query string, limit int) ([]models.SearchResult, error) {
	// COALESCE(s.description, '') inside the similarity() concatenation
	// (finding 7, extended beyond the reviewer's literal fix text after live
	// testing surfaced the FULL defect, §11.4.194): PostgreSQL's `||` yields
	// NULL when EITHER operand is NULL, so with a nullable description
	// (migrations/001_initial.up.sql) a NULL-description row's ENTIRE
	// concatenation -- and therefore its similarity()/score -- becomes NULL,
	// independent of whatever the raw `s.description` SELECT column scans as.
	// Scanning that NULL score into the non-nullable float64 Score field
	// panics/errors "cannot scan NULL into *float64" BEFORE the description
	// column is ever reached -- proven against a real NULL-description row
	// (§11.4.199 exact reproduction), which is why the NullString fix on
	// Description ALONE (below) does not by itself prevent this query from
	// erroring on such a row.
	sql := `
		SELECT s.id, s.name, s.version, s.title, s.description, s.content,
		       s.metadata, s.status, s.kind, s.created_at, s.updated_at,
		       similarity(s.name || ' ' || s.title || ' ' || COALESCE(s.description, ''), $1) as score
		FROM skills s
		WHERE s.name % $1 OR s.title % $1 OR s.description ILIKE '%' || $1 || '%'
		ORDER BY score DESC, s.name
		LIMIT $2
	`
	rows, err := s.pool.Query(ctx, sql, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	var results []models.SearchResult
	for rows.Next() {
		var r models.SearchResult
		var metadata []byte
		// F2-precedent NullString scan (Fable code-review remediation, finding
		// 7): description is a nullable TEXT column (migrations/001_initial.up.sql)
		// that Store.Create's INSERT always sets to a Go zero-value "" (never
		// SQL NULL) -- but a row written by direct SQL (bypassing Create;
		// e.g. a test fixture, a migration seed, or a future bulk-loader) can
		// leave it genuinely NULL, and scanning a NULL directly into
		// models.Skill's plain (non-nullable) Description string then panics
		// with "cannot scan NULL into *string", exactly as GetByName's
		// fetched_hash/content_cached fix (store.go's GetByName) already
		// established for resources. Scanning through sql.NullString and
		// defaulting to "" on NULL matches that same precedent here.
		var description dbsql.NullString
		err := rows.Scan(
			&r.Skill.ID, &r.Skill.Name, &r.Skill.Version, &r.Skill.Title,
			&description, &r.Skill.Content, &metadata,
			&r.Skill.Status, &r.Skill.Kind, &r.Skill.CreatedAt, &r.Skill.UpdatedAt,
			&r.Score,
		)
		if err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		r.Skill.Description = description.String
		r.Skill.Metadata = metadata
		results = append(results, r)
	}
	// F4 (Fable code-review remediation, finding 4): rows.Next() returning
	// false means EITHER the result set is exhausted OR row iteration was cut
	// short by a driver/connection-level error -- the two are indistinguishable
	// from the loop alone. Without checking rows.Err(), a mid-stream failure
	// silently truncates `results` to whatever was scanned so far and this
	// function returns that partial slice with a nil error, masking the
	// failure as "these are all the matches" instead of surfacing it. Mirrors
	// the sibling package's own convention (internal/db/vector.go).
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search skills rows: %w", err)
	}

	if len(results) == 0 {
		// Fallback: return all skills if no similarity match
		sql = `
			SELECT s.id, s.name, s.version, s.title, s.description, s.content,
			       s.metadata, s.status, s.kind, s.created_at, s.updated_at, 0.0 as score
			FROM skills s
			WHERE s.name ILIKE '%' || $1 || '%' OR s.title ILIKE '%' || $1 || '%'
			ORDER BY s.name
			LIMIT $2
		`
		rows, err := s.pool.Query(ctx, sql, query, limit)
		if err != nil {
			return nil, fmt.Errorf("fallback search: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r models.SearchResult
			var metadata []byte
			var description dbsql.NullString // F2/finding 7 precedent, see above
			if err := rows.Scan(
				&r.Skill.ID, &r.Skill.Name, &r.Skill.Version, &r.Skill.Title,
				&description, &r.Skill.Content, &metadata,
				&r.Skill.Status, &r.Skill.Kind, &r.Skill.CreatedAt, &r.Skill.UpdatedAt,
				&r.Score,
			); err != nil {
				return nil, fmt.Errorf("scan fallback result: %w", err)
			}
			r.Skill.Description = description.String
			r.Skill.Metadata = metadata
			results = append(results, r)
		}
		// F4/finding 4, fallback leg: see the primary leg's rows.Err() note above.
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("fallback search rows: %w", err)
		}
	}

	return results, nil
}

// fuseSearchResults merges the vector-KNN and keyword candidate lists into one
// ranked list with weighted Reciprocal Rank Fusion (§G29). Each list is assumed
// to already be in descending-relevance order (index 0 = best). A skill present
// in both lists accrues both weighted 1/(rrfK+rank+1) contributions; ties are
// broken deterministically by skill name so the ordering is stable across runs.
// The fused relevance replaces each result's per-leg Score. limit ≤ 0 means no
// cap.
func fuseSearchResults(vector, trigram []models.SearchResult, limit int) []models.SearchResult {
	type agg struct {
		res   models.SearchResult
		score float64
	}
	byID := make(map[uuid.UUID]*agg)
	order := make([]uuid.UUID, 0, len(vector)+len(trigram))

	accumulate := func(list []models.SearchResult, weight float64) {
		for rank, r := range list {
			a, ok := byID[r.Skill.ID]
			if !ok {
				cp := r
				a = &agg{res: cp}
				byID[r.Skill.ID] = a
				order = append(order, r.Skill.ID)
			}
			a.score += weight / float64(rrfK+rank+1)
		}
	}
	accumulate(vector, vectorRRFWeight)
	accumulate(trigram, trigramRRFWeight)

	merged := make([]models.SearchResult, 0, len(order))
	for _, id := range order {
		a := byID[id]
		a.res.Score = a.score
		merged = append(merged, a.res)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].Skill.Name < merged[j].Skill.Name
	})

	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// GetByName retrieves a complete skill by its unique name.
func (s *Store) GetByName(ctx context.Context, name string) (*models.Skill, error) {
	sql := `
		SELECT s.id, s.name, s.version, s.title, s.description, s.content,
		       s.metadata, s.status, s.kind, s.created_at, s.updated_at
		FROM skills s
		WHERE s.name = $1
	`
	var skill models.Skill
	var metadata []byte
	err := s.pool.QueryRow(ctx, sql, name).Scan(
		&skill.ID, &skill.Name, &skill.Version, &skill.Title,
		&skill.Description, &skill.Content, &metadata,
		&skill.Status, &skill.Kind, &skill.CreatedAt, &skill.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Wrap the ErrSkillNotFound sentinel (§11.4.6/§11.4.102 forensic
			// finding, pre-existing defect discovered while implementing the
			// P1.T1 M10 seed-import test): ImportFromTOML's existing-skill
			// guard (import_export.go) checks errors.Is(err, ErrSkillNotFound)
			// to distinguish "not found, OK to create" from a real DB error.
			// The previous plain fmt.Errorf (no %w) never satisfied that
			// check, so EVERY ImportFromTOML call for a brand-new skill name
			// took the "real error" branch and aborted before the INSERT ever
			// ran -- the message text is unchanged, only the wrapping is
			// fixed. Blast-radius audited (N3 correction, Fable code-review
			// remediation): every non-test GetByName call site in the repo,
			// 9 in total across 6 files --
			// import_export.go:40 (ImportFromTOML existing-skill guard,
			// the one that actually needs errors.Is), import_export.go:235
			// (ExportToTOML), graph.go:192 (GetDependencyTree),
			// store.go:201 (GetTree, this same package), pipeline.go:174,246,424
			// (internal/autoexpand, 3 call sites), mcp/tools.go:117
			// (skill_get tool), and main.go:218 (REST skill-by-name route) --
			// all treat a GetByName error either generically (fmt.Errorf-wrap,
			// HTTP 404/500, or a boolean "not found") or via errors.Is on this
			// exact sentinel; none depended on the old unwrapped string form.
			return nil, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
		}
		return nil, fmt.Errorf("get skill: %w", err)
	}
	skill.Metadata = metadata

	// Load dependencies
	// G07 (research/g06_g07_skilltree_dag_design.md §4c): a DETERMINISTIC
	// ORDER BY is required for a byte-stable ExportToTOML round-trip
	// (§2.3(3)). Without it row order is query-plan-dependent, so two exports
	// of the same skill (or two skills carrying the same edge set) could emit
	// the typed-edge lists / [[skill.components]] entries in different orders.
	// Order by relation_type, then sort_order (the umbrella→component ordering,
	// NULLS LAST so unordered edges trail) then the target name as the stable
	// final tiebreak.
	depsSQL := `
		SELECT sd.skill_id, sd.depends_on, sd.relation_type, sd.optional, sd.sort_order,
		       ds.name as depends_on_name, ds.title as depends_on_title
		FROM skill_dependencies sd
		JOIN skills ds ON sd.depends_on = ds.id
		WHERE sd.skill_id = $1
		ORDER BY sd.relation_type, sd.sort_order NULLS LAST, ds.name
	`
	depRows, err := s.pool.Query(ctx, depsSQL, skill.ID)
	if err != nil {
		return nil, fmt.Errorf("get dependencies: %w", err)
	}
	defer depRows.Close()
	for depRows.Next() {
		var d models.SkillDependency
		if err := depRows.Scan(&d.SkillID, &d.DependsOn, &d.RelationType, &d.Optional, &d.SortOrder, &d.DependsOnName, &d.DependsOnTitle); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		skill.Dependencies = append(skill.Dependencies, d)
	}

	// Load resources
	// G07 §4c: deterministic ordering for byte-stable export (see depsSQL note).
	// url alone is NOT a total order — resources.url has no unique constraint
	// (migrations/001_initial.up.sql), so two resources may share a url while
	// differing in title/resource_type (the only other columns the export emits,
	// see ExportToTOML "Map resources"). id is a v4-random UUID re-minted on every
	// ImportFromTOML, so `ORDER BY url, id` reorders such same-url rows across an
	// export→import→export, breaking the §2.3(3) byte-stability contract. Order by
	// the STABLE emitted columns first (url, title, resource_type); the residual
	// id tiebreak then only ever separates BYTE-IDENTICAL exported rows, so the
	// ordering is a total order that is swap-invariant on the export.
	resSQL := `
		SELECT id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at
		FROM resources WHERE skill_id = $1
		ORDER BY url, title, resource_type, id
	`
	resRows, err := s.pool.Query(ctx, resSQL, skill.ID)
	if err != nil {
		return nil, fmt.Errorf("get resources: %w", err)
	}
	defer resRows.Close()
	for resRows.Next() {
		var r models.Resource
		// B1 fix (Fable code-review remediation): fetched_hash/content_cached
		// are nullable TEXT columns (migrations/001_initial.up.sql) that
		// store.go never sets on INSERT until validation/caching runs
		// (internal/skill/resources.go), so a freshly-imported resource --
		// including every one imported via ImportFromTOML, which the B1 fix
		// makes non-empty for the first time -- has them NULL. Scanning a SQL
		// NULL directly into models.Resource's plain (non-nullable) string
		// fields panics/errors ("cannot scan NULL into *string"); this was
		// never exercised before because ImportFromTOML's resources always
		// decoded empty pre-fix, so GetByName never had a real resource row
		// to load. Scan through sql.NullString and default to "" (the same
		// value NewResource-class helpers already use for an unset hash, see
		// resources.go's own `SET content_cached = '', fetched_hash = ''`
		// reset), so this genuinely resolves the resource-import deadness end
		// to end rather than trading one silent gap for a crash.
		var fetchedHash, contentCached dbsql.NullString
		if err := resRows.Scan(&r.ID, &r.SkillID, &r.URL, &r.Title, &r.ResourceType, &fetchedHash, &contentCached, &r.LastValidated, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		r.FetchedHash = fetchedHash.String
		r.ContentCached = contentCached.String
		skill.Resources = append(skill.Resources, r)
	}

	return &skill, nil
}

// GetTree returns the dependency tree for a skill up to the specified depth.
// Uses a single recursive CTE to fetch all reachable skills and edges,
// then assembles the tree in Go — O(1) queries instead of O(N) (§11.4.82).
func (s *Store) GetTree(ctx context.Context, name string, maxDepth int) (*models.SkillTreeNode, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 50 {
		maxDepth = 50 // Hard cap to prevent runaway queries
	}

	// Fetch the root skill
	rootSkill, err := s.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}

	// Single recursive CTE: fetch all reachable skills + their dependency edges
	// in one round-trip, eliminating the N+1 pattern.
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE dep_tree AS (
			SELECT
				s.id, s.name, s.version, s.title, s.description, s.content,
				s.metadata, s.status, s.kind, s.created_at, s.updated_at,
				sd.relation_type, sd.optional, sd.sort_order,
				0 AS depth
			FROM skill_dependencies sd
			JOIN skills s ON s.id = sd.depends_on
			WHERE sd.skill_id = $1

			UNION ALL

			SELECT
				s.id, s.name, s.version, s.title, s.description, s.content,
				s.metadata, s.status, s.kind, s.created_at, s.updated_at,
				sd.relation_type, sd.optional, sd.sort_order,
				dt.depth + 1
			FROM skill_dependencies sd
			JOIN skills s ON s.id = sd.depends_on
			JOIN dep_tree dt ON dt.id = sd.skill_id
			WHERE dt.depth + 1 < $2
		)
		SELECT id, name, version, title, description, content, metadata,
		       status, kind, created_at, updated_at,
		       relation_type, optional, sort_order, depth
		FROM dep_tree
		ORDER BY depth, name
	`, rootSkill.ID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("recursive tree query: %w", err)
	}
	defer rows.Close()

	// Collect all reachable nodes and their edges
	type nodeInfo struct {
		skill models.Skill
		depth int
	}
	nodeMap := make(map[uuid.UUID]*nodeInfo)
	// parentID -> []childID (ordered)
	childMap := make(map[uuid.UUID][]uuid.UUID)
	// Track which edges we've seen to avoid duplicates in the childMap
	seenEdges := make(map[string]bool)

	for rows.Next() {
		var sk models.Skill
		var metadata []byte
		var relType models.DependencyType
		var optional bool
		var sortOrder *int
		var depth int
		if err := rows.Scan(
			&sk.ID, &sk.Name, &sk.Version, &sk.Title,
			&sk.Description, &sk.Content, &metadata,
			&sk.Status, &sk.Kind, &sk.CreatedAt, &sk.UpdatedAt,
			&relType, &optional, &sortOrder, &depth,
		); err != nil {
			return nil, fmt.Errorf("scan tree node: %w", err)
		}
		sk.Metadata = metadata

		if _, exists := nodeMap[sk.ID]; !exists {
			nodeMap[sk.ID] = &nodeInfo{skill: sk, depth: depth}
		}

		// Record the edge: we need to figure out the parent from the CTE.
		// The CTE joins on sd.skill_id -> sd.depends_on, so for each row
		// the parent is the skill_id that led to this depends_on.
		// We'll reconstruct edges from the dependency data below.
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tree rows: %w", err)
	}

	// Now fetch edges among the closure nodes to build parent->child relationships
	if len(nodeMap) > 0 {
		ids := make([]uuid.UUID, 0, len(nodeMap))
		for id := range nodeMap {
			ids = append(ids, id)
		}

		edgeRows, err := s.pool.Query(ctx, `
			SELECT skill_id, depends_on, relation_type, optional, sort_order,
			       ds.name as depends_on_name, ds.title as depends_on_title
			FROM skill_dependencies sd
			JOIN skills ds ON ds.id = sd.depends_on
			WHERE sd.skill_id = $1
			ORDER BY sd.relation_type, sd.sort_order NULLS LAST, ds.name
		`, rootSkill.ID)
		if err != nil {
			return nil, fmt.Errorf("query root edges: %w", err)
		}

		// Collect root's direct edges
		rootSkill.Dependencies = nil
		for edgeRows.Next() {
			var d models.SkillDependency
			if err := edgeRows.Scan(&d.SkillID, &d.DependsOn, &d.RelationType, &d.Optional, &d.SortOrder, &d.DependsOnName, &d.DependsOnTitle); err != nil {
				edgeRows.Close()
				return nil, fmt.Errorf("scan root edge: %w", err)
			}
			if _, ok := nodeMap[d.DependsOn]; ok {
				rootSkill.Dependencies = append(rootSkill.Dependencies, d)
				edgeKey := fmt.Sprintf("%s:%s:%s", d.SkillID, d.DependsOn, d.RelationType)
				if !seenEdges[edgeKey] {
					childMap[d.SkillID] = append(childMap[d.SkillID], d.DependsOn)
					seenEdges[edgeKey] = true
				}
			}
		}
		edgeRows.Close()

		// Fetch edges for all non-root nodes in the closure
		if len(ids) > 1 {
			// Include root ID so we get its edges too (already handled above, but
			// this covers non-root nodes' edges)
			allEdgeRows, err := s.pool.Query(ctx, `
				SELECT skill_id, depends_on, relation_type, optional, sort_order,
				       ds.name as depends_on_name, ds.title as depends_on_title
				FROM skill_dependencies sd
				JOIN skills ds ON ds.id = sd.depends_on
				WHERE sd.skill_id = ANY($1)
				ORDER BY sd.skill_id, sd.relation_type, sd.sort_order NULLS LAST, ds.name
			`, ids)
			if err != nil {
				return nil, fmt.Errorf("query all edges: %w", err)
			}
			for allEdgeRows.Next() {
				var d models.SkillDependency
				if err := allEdgeRows.Scan(&d.SkillID, &d.DependsOn, &d.RelationType, &d.Optional, &d.SortOrder, &d.DependsOnName, &d.DependsOnTitle); err != nil {
					allEdgeRows.Close()
					return nil, fmt.Errorf("scan edge: %w", err)
				}
				if _, ok := nodeMap[d.DependsOn]; ok {
					edgeKey := fmt.Sprintf("%s:%s:%s", d.SkillID, d.DependsOn, d.RelationType)
					if !seenEdges[edgeKey] {
						childMap[d.SkillID] = append(childMap[d.SkillID], d.DependsOn)
						seenEdges[edgeKey] = true
					}
				}
			}
			if err := allEdgeRows.Err(); err != nil {
				allEdgeRows.Close()
				return nil, fmt.Errorf("iterate all edges: %w", err)
			}
			allEdgeRows.Close()
		}
	}

	// Assemble the tree recursively from the closure
	root := &models.SkillTreeNode{
		Skill:    *rootSkill,
		Depth:    0,
		Children: []models.SkillTreeNode{},
	}

	seen := map[uuid.UUID]bool{rootSkill.ID: true}
	var attach func(parent *models.SkillTreeNode, parentID uuid.UUID, depth int)
	attach = func(parent *models.SkillTreeNode, parentID uuid.UUID, depth int) {
		for _, childID := range childMap[parentID] {
			if seen[childID] {
				continue
			}
			ni, ok := nodeMap[childID]
			if !ok {
				continue
			}
			seen[childID] = true
			parent.Children = append(parent.Children, models.SkillTreeNode{
				Skill:    ni.skill,
				Depth:    depth,
				Children: []models.SkillTreeNode{},
			})
			attach(&parent.Children[len(parent.Children)-1], childID, depth+1)
		}
	}
	attach(root, rootSkill.ID, 1)

	return root, nil
}

// Create inserts a new skill into the database.
func (s *Store) Create(ctx context.Context, skill *models.Skill) error {
	if skill.ID == uuid.Nil {
		skill.ID = uuid.New()
	}

	metadataJSON, err := json.Marshal(skill.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	sql := `
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
	`
	var returnedID uuid.UUID
	err = s.pool.QueryRow(ctx, sql,
		skill.ID, skill.Name, skill.Version, skill.Title,
		skill.Description, skill.Content, metadataJSON,
		skill.Status, skill.Kind.NormalizeOrAtomic(),
	).Scan(&returnedID)
	if err != nil {
		return fmt.Errorf("create skill: %w", err)
	}

	// Insert dependencies in a SINGLE round-trip using a multi-row VALUES
	// clause. ON CONFLICT targets the widened (skill_id, depends_on,
	// relation_type) primary key introduced by
	// migrations/002_granularity.up.sql — a pair may now carry more than one
	// typed edge (e.g. both `requires` and `recommends`), so the old
	// (skill_id, depends_on) conflict target no longer matches any unique
	// index (research/p1t1_granularity_schema_migration.md §2 L3).
	if len(skill.Dependencies) > 0 {
		args := make([]interface{}, 0, 1+len(skill.Dependencies)*4)
		args = append(args, returnedID)
		var buf strings.Builder
		buf.WriteString(`INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional, sort_order) VALUES `)
		for i, dep := range skill.Dependencies {
			if i > 0 {
				buf.WriteByte(',')
			}
			base := 1 + i*4
			fmt.Fprintf(&buf, "($1,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4)
			args = append(args, dep.DependsOn, string(dep.RelationType), dep.Optional, dep.SortOrder)
		}
		buf.WriteString(` ON CONFLICT (skill_id, depends_on, relation_type) DO UPDATE SET optional = EXCLUDED.optional, sort_order = EXCLUDED.sort_order`)
		if _, err := s.pool.Exec(ctx, buf.String(), args...); err != nil {
			return fmt.Errorf("create dependencies: %w", err)
		}
	}

	// Upsert registry entry
	regSQL := `
		INSERT INTO skill_registry (skill_id, skill_name, missing_deps, stale, auto_expand, coverage)
		VALUES ($1, $2, '{}', false, true, 0.0)
		ON CONFLICT (skill_id) DO NOTHING
	`
	_, err = s.pool.Exec(ctx, regSQL, returnedID, skill.Name)
	if err != nil {
		return fmt.Errorf("create registry entry: %w", err)
	}

	skill.ID = returnedID

	// §G59 write-through: db.StoreSkillEmbedding (internal/db/vector.go) had
	// ZERO callers project-wide before this fix -- Create never wrote a
	// skill's embedding column, so every newly created skill silently
	// degraded to trigram-only search even when a query-side embedder was
	// configured (the vector-KNN leg of Search, §G29, never had anything of
	// its OWN to retrieve for a skill created after that fix landed; every
	// populated embedding in the existing test suite is written by a raw SQL
	// UPDATE in test setup, specifically because Create never did it). A nil
	// embedder (the default: NewStore never sets one, mirroring Search's own
	// `s.embedder == nil` early return) is a documented no-op -- every
	// existing caller that never opts in via WithEmbedder keeps the exact
	// pre-G59 behaviour, embedding column stays NULL, and no embedder method
	// is ever invoked.
	if s.embedder != nil {
		s.embedWriteThrough(ctx, skill)
	}

	return nil
}

// embedWriteThrough computes and persists a skill's write-side embedding
// immediately after Create has durably written its skill row (and, when
// present, its dependency/registry rows above) -- the write-side counterpart
// to Search's query-side embedding (§G59). Called only when s.embedder != nil
// (see Create); safe to call at most once per Create invocation.
//
// TEXT REPRESENTATION: buildSkillEmbedText concatenates Name/Title/
// Description/Content -- the richest available semantic representation of
// the skill, extending the fields textSearch's own trigram formula already
// concatenates (`s.name || ' ' || s.title || ' ' || COALESCE(s.description,
// ”)`) with Content, which typically carries the skill's actual technical
// substance. This is deliberately NOT the same string Search embeds for a
// QUERY: Search embeds the caller's raw free-text query (e.g. "docker
// container security"), and there is no equivalent "skill's own text" on the
// query side to literally match character-for-character -- a document
// embedding and a query embedding are, by construction, built from different
// text. What DOES have to match -- and does -- is that both are produced by
// the SAME embedder instance (same model, same Dimensions()): Search's query
// embed and this write-side embed both go through the one s.embedder
// configured on this Store, so the two vector spaces are directly comparable
// via cosine similarity. That shared-embedder guarantee, not textual
// identity, is what makes vector-KNN meaningful across the write and query
// sides; embedding identical text on both ends is neither necessary nor how
// any embedding-based (as opposed to lexical) retrieval system operates.
//
// FAILURE POSTURE (operator-specified; reviewer may adjudicate a different
// choice -- documented here per that instruction): a failure in EITHER the
// embedder call OR the subsequent database write NEVER fails the enclosing
// Create call. This mirrors Search's existing posture toward a query-time
// embedder outage (warnEmbeddingDegraded + continue on the trigram-only
// result set) applied symmetrically to the write side: an embedding-provider
// outage (or a transient failure persisting the vector) degrades this ONE
// skill to trigram-only searchability -- exactly like every skill created
// before an embedder was ever configured -- rather than blocking skill
// creation outright. A skill that lands without an embedding (re-embeddable
// later by a future backfill pass) is strictly more useful to callers than no
// skill at all; and per the no-guessing mandate, a real failure is never
// silently swallowed with zero telemetry -- it reuses the SAME throttled
// warnEmbeddingDegraded sink Search already writes to (WithLogger-injected,
// never the package-level zap.L() no-op default), so a sustained
// embedding-provider outage remains observable during CREATEs, not only
// during searches.
//
// STALE-VECTOR-ON-UPDATE (F3, code-review remediation): Create's upsert SET
// list never touches `embedding` (see the SQL below), so on the
// ON CONFLICT (name) DO UPDATE branch -- an existing skill re-Created with
// changed content -- a failure/skip on THIS call leaves whatever vector a
// PRIOR successful embed wrote still in place, now stale against the NEW
// content. "Degrades to trigram-only (NULL)" is only accurate for a fresh
// INSERT, where the column starts NULL; for an UPDATE it must be made true by
// actively clearing the column, not merely by not-writing to it. ALL FOUR
// failure/skip branches below therefore call clearStaleEmbedding -- (1) empty
// buildSkillEmbedText text, (2) s.embedder.Embed itself returning an error,
// (3) Embed returning no error but an empty/unusable vector, and (4) Embed
// succeeding but the subsequent db.StoreSkillEmbedding write failing (F3
// round-2 code-review remediation; e.g. the embedder returns a vector whose
// dimension does not match the `embedding vector(768)` column) -- each call
// a harmless no-op on a fresh insert (already NULL) and an honest degrade on
// an update (clears the stale vector rather than silently serving it).
//
// NOT a single atomic SQL transaction: this call deliberately does NOT wrap
// the skill-row/dependency/registry writes above and the embedding UPDATE in
// one BEGIN/COMMIT block. An all-or-nothing transaction would roll back the
// just-created skill row on any embedder hiccup -- the OPPOSITE of the
// degrade-gracefully posture this method exists to implement. "Same
// transaction as the skill row write" is satisfied in the sense that matters
// here: this call happens synchronously, in the same Create() invocation,
// immediately after the skill row is durably committed -- never a separate,
// out-of-band, possibly-never-run backfill job -- so by the time Create
// returns, the embedding write has already been attempted (and, on success,
// persisted) for every caller with an embedder configured.
//
// CONCURRENT-SAME-NAME-CREATE BOUNDARY (W2, round-3 Fable-xhigh re-review,
// documented honestly here, deliberately NOT guarded this round -- tracked
// separately): precisely because this is NOT one atomic transaction (see
// above) and neither Create nor embedWriteThrough take any lock serializing
// two concurrent Create calls for the SAME skill name, two callers racing an
// ON CONFLICT (name) DO UPDATE for that name can interleave their skill-row
// upsert and their embedding write out of order -- e.g. caller A's upsert
// commits, caller B's upsert commits (B's content now wins the row), then
// A's embedWriteThrough finishes its (slower) embed and writes ITS
// (A's, now-superseded) vector last. The result is a vector-KNN-servable
// embedding computed from content that is NO LONGER what the row stores --
// stale on an ALL-SUCCESS path, not merely on one of the failure/skip paths
// clearStaleEmbedding actively guards above. This is a genuinely different
// defect class from F3/F5 (which are failure/skip paths this method already
// closes): it is inherent in the current non-transactional, unlocked
// write-through design and requires either serializing same-name Creates or
// fencing the embedding write against the row's updated_at (only apply the
// write if updated_at still matches what THIS call's own upsert just wrote).
// Implementing that fence is out of scope for this round; it is tracked as a
// separate follow-up rather than silently left undocumented (§11.4.6).
func (s *Store) embedWriteThrough(ctx context.Context, sk *models.Skill) {
	// Defensive precondition re-check (code-review NIT): every current call
	// site (Create, and ImportFromTOML via the same guard) already checks
	// s.embedder != nil before calling this method, so this is unreachable
	// today -- but documenting and enforcing the precondition here means a
	// future call site that forgets the guard degrades cleanly (nil-safe
	// no-op) instead of nil-pointer-panicking on s.embedder.Embed below.
	if s.embedder == nil {
		return
	}

	text := buildSkillEmbedText(sk)
	if text == "" {
		// Nothing meaningful to embed. On an ON CONFLICT (name) DO UPDATE
		// call this must still CLEAR any embedding a PRIOR version of this
		// skill wrote (F3) -- a no-op on a fresh insert, see clearStaleEmbedding.
		s.clearStaleEmbedding(ctx, sk)
		return
	}

	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		s.warnEmbeddingDegraded(fmt.Errorf("create-time embed for skill %q: %w", sk.Name, err))
		s.clearStaleEmbedding(ctx, sk)
		return
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		// The provider returned no error but also no usable vector: nothing to
		// store, and (mirroring Search's own handling of this exact case) not
		// itself a failure signal worth warning about. Still clear any stale
		// vector from a prior successful embed (F3).
		s.clearStaleEmbedding(ctx, sk)
		return
	}

	if err := db.StoreSkillEmbedding(ctx, s.pool, sk.ID, pgvector.NewVector(vecs[0])); err != nil {
		s.warnEmbeddingDegraded(fmt.Errorf("create-time store embedding for skill %q: %w", sk.Name, err))
		// F3 round-2 (code-review MEDIUM finding): this branch -- Embed()
		// SUCCEEDED but the subsequent database write did not (e.g. the
		// embedder returned a vector whose dimension does not match the
		// `embedding vector(768)` column) -- must clear a stale PRIOR vector
		// exactly like the other three failure/skip branches above, per this
		// method's own doc comment ("every failure/skip branch below
		// therefore calls clearStaleEmbedding"). Before this fix it did not:
		// on an ON CONFLICT (name) DO UPDATE call, a failed store here left
		// whatever vector an EARLIER successful Create wrote in place, now
		// stale against the skill's NEW content. "The clear would fail
		// anyway" does NOT hold here -- ClearSkillEmbedding writes NULL,
		// which has no dimension to violate, so it succeeds even though the
		// store that just failed did not.
		//
		// F5 mechanism fix (round-3 Fable-xhigh re-review, MEDIUM, PROVEN
		// LIVE): this call site used to be the ONLY one of the four
		// clearStaleEmbedding call sites in this method that detached from
		// ctx's cancellation (via context.WithoutCancel) before clearing --
		// branches 1-3 above passed the raw, still-attached-to-the-caller ctx
		// straight through. That asymmetry was proven exploitable: a caller
		// whose ctx is canceled DURING the (slow) s.embedder.Embed call above
		// (e.g. an HTTP client disconnect -- the realistic production
		// trigger) makes Embed return ctx.Err(), which is handled by the
		// Embed-error branch immediately above and calls clearStaleEmbedding
		// with that SAME already-canceled ctx; the clear's own UPDATE then
		// failed instantly on the dead context, leaving the PREVIOUS
		// content's vector in place and still vector-KNN-servable -- exactly
		// the stale-vector defect this method exists to prevent, and on the
		// branch MOST likely to be hit in production (Embed is the slow,
		// network-bound step most exposed to a mid-flight client disconnect).
		// The detach-from-caller-cancellation + bounded-timeout logic now
		// lives INSIDE clearStaleEmbedding itself (see its doc comment) so it
		// applies uniformly to ALL FOUR call sites -- this call site
		// therefore passes the plain ctx, exactly like the other three.
		s.clearStaleEmbedding(ctx, sk)
	}
}

// clearStaleEmbeddingTimeout bounds the clear-embedding write below (W1,
// round-3 Fable-xhigh re-review) so a detached clear can never hang
// indefinitely against an unreachable/stuck database. This codebase has no
// existing PER-STATEMENT statement_timeout convention to draw the value
// from -- internal/config's DatabaseConfig only sets ConnectTimeout, consumed
// by DSNWithTimeout/postgres.go's New for the INITIAL pool dial, never for
// individual queries -- so 5 seconds is not lifted from an existing
// statement-timeout config. It mirrors this codebase's closest actual
// precedent for bounding a single, small, indexed-primary-key DB statement:
// Pool.Health (internal/db/postgres.go) wraps its own single-statement op (a
// Ping) in context.WithTimeout(ctx, 5*time.Second). clearStaleEmbedding's
// UPDATE is the same shape -- one row, one indexed primary-key predicate --
// so the same budget is reused here rather than inventing an unrelated
// number (§11.4.6: no silent magic constant).
const clearStaleEmbeddingTimeout = 5 * time.Second

// clearStaleEmbedding sets sk's embedding column to NULL (§G59 F3, code-review
// remediation). Called from ALL FOUR embedWriteThrough failure/skip paths --
// empty text, Embed error, empty vector, AND (F3 round-2 remediation) a
// failing db.StoreSkillEmbedding write after a successful Embed -- so a
// re-embed attempted during an ON CONFLICT (name) DO UPDATE never leaves the
// PREVIOUS content's vector in place, now mismatched against the skill's NEW
// content -- serving a stale, semantically-wrong vector-KNN match is worse
// than honestly degrading to trigram-only. A no-op-equivalent on a fresh
// insert (the column is already NULL by column default).
//
// DETACHED + BOUNDED (F5 mechanism fix, round-3 Fable-xhigh re-review,
// MEDIUM, PROVEN LIVE): the incoming ctx is deliberately never used directly
// for the clear write below. ctx is whichever of embedWriteThrough's four
// call sites invoked this method, and for three of those four (the
// empty-text, Embed-error, and empty-vector branches) it is the SAME ctx the
// caller handed to the enclosing Create/embedWriteThrough call -- almost
// certainly a per-request context (e.g. an HTTP handler's r.Context())
// whose cancellation is entirely unrelated to whether it remains correct to
// clear a stale vector on a skill row Create has ALREADY durably committed.
// Detaching HERE, once, inside clearStaleEmbedding itself -- rather than at
// each individual call site, as the pre-round-3 code did for only ONE of the
// four -- closes this uniformly for all four; no embedWriteThrough call site
// needs to know about or separately apply this treatment.
// context.WithoutCancel (go.mod: go 1.25.5) preserves ctx's VALUES (e.g. any
// request-scoped tracing) while stripping its Done()/Err(), so this clear
// gets its own chance to complete independent of the caller's lifetime.
//
// A context.WithoutCancel-detached context by itself carries NO deadline at
// all (W1, round-3 Fable-xhigh re-review): without an explicit bound, a
// detached clear against a stuck/unreachable database would hang
// indefinitely, trading one failure mode (a fast, honest failure on an
// already-canceled ctx) for a worse one (an unbounded hang).
// clearStaleEmbeddingTimeout (above) bounds this call to a short, explicit
// budget instead.
//
// A failure to clear (including a timeout under clearStaleEmbeddingTimeout)
// is reported via warnEmbeddingClearFailed -- a DISTINCT, separately
// throttled sink from warnEmbeddingDegraded (F6, round-3 Fable-xhigh
// re-review, LOW; see warnEmbeddingClearFailed's own doc comment for why
// sharing the throttle counter would make this warning permanently
// invisible) -- never silently swallowed.
func (s *Store) clearStaleEmbedding(ctx context.Context, sk *models.Skill) {
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clearStaleEmbeddingTimeout)
	defer cancel()
	if err := db.ClearSkillEmbedding(clearCtx, s.pool, sk.ID); err != nil {
		s.warnEmbeddingClearFailed(fmt.Errorf("clear stale embedding for skill %q: %w", sk.Name, err))
	}
}

// buildSkillEmbedText assembles the write-side textual representation
// embedded into a skill's vector column by embedWriteThrough (§G59). See that
// method's doc comment for the full rationale (why these fields, and why this
// need not -- and structurally cannot -- be textually identical to what
// Search embeds for a query).
func buildSkillEmbedText(sk *models.Skill) string {
	parts := make([]string, 0, 4)
	if sk.Name != "" {
		parts = append(parts, sk.Name)
	}
	if sk.Title != "" {
		parts = append(parts, sk.Title)
	}
	if sk.Description != "" {
		parts = append(parts, sk.Description)
	}
	if sk.Content != "" {
		parts = append(parts, sk.Content)
	}
	return strings.Join(parts, " ")
}

// CreateFromTOML creates a skill from a TOML skill wrapper.
//
// §G59: CreateFromTOML delegates to Create (below) for the actual skill-row
// write, so it inherits Create's embedding write-through automatically -- no
// separate embedWriteThrough call is needed here. There is likewise no
// separate `Update` method on Store: Create's `ON CONFLICT (name) DO UPDATE`
// clause IS this package's skill update path (an upsert keyed on the unique
// `name` column), so wiring the embedding write into Create alone covers
// create, update, and CreateFromTOML uniformly.
func (s *Store) CreateFromTOML(ctx context.Context, wrapper *models.TOMLSkillWrapper) (*models.Skill, error) {
	metadataJSON, _ := json.Marshal(wrapper.Skill.Metadata)

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

	// Resolve dependencies in a SINGLE batch query instead of N individual
	// lookups (performance: eliminates N+1 round-trips for dependency
	// resolution).
	depNames := append(
		append(
			append([]string{}, wrapper.Skill.Dependencies.Requires...),
			wrapper.Skill.Dependencies.Extends...),
		wrapper.Skill.Dependencies.Recommends...)
	if len(depNames) > 0 {
		depNameToID := make(map[string]uuid.UUID, len(depNames))
		rows, err := s.pool.Query(ctx, `SELECT id, name FROM skills WHERE name = ANY($1)`, depNames)
		if err != nil {
			return nil, fmt.Errorf("batch resolve dependency names: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan resolved dep: %w", err)
			}
			depNameToID[name] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate resolved deps: %w", err)
		}
		for _, depName := range wrapper.Skill.Dependencies.Requires {
			depID, ok := depNameToID[depName]
			if !ok {
				return nil, fmt.Errorf("resolve dependency %q: skill not found", depName)
			}
			skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
				SkillID: skill.ID, DependsOn: depID, RelationType: models.DepTypeRequires,
			})
		}
		for _, depName := range wrapper.Skill.Dependencies.Extends {
			depID, ok := depNameToID[depName]
			if !ok {
				return nil, fmt.Errorf("resolve dependency %q: skill not found", depName)
			}
			skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
				SkillID: skill.ID, DependsOn: depID, RelationType: models.DepTypeExtends,
			})
		}
		for _, depName := range wrapper.Skill.Dependencies.Recommends {
			depID, ok := depNameToID[depName]
			if !ok {
				return nil, fmt.Errorf("resolve dependency %q: skill not found", depName)
			}
			skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
				SkillID: skill.ID, DependsOn: depID, RelationType: models.DepTypeRecommends,
			})
		}
	}

	// Add resources
	for _, r := range wrapper.Skill.Resources {
		skill.Resources = append(skill.Resources, models.Resource{
			ID:           uuid.New(),
			SkillID:      skill.ID,
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		})
	}

	if err := s.Create(ctx, skill); err != nil {
		return nil, err
	}

	return skill, nil
}

func (s *Store) resolveSkillID(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, "SELECT id FROM skills WHERE name = $1", name).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return uuid.Nil, fmt.Errorf("skill %q not found", name)
		}
		return uuid.Nil, err
	}
	return id, nil
}

// GetMissingSkills returns skills with missing dependencies (gaps in the graph).
func (s *Store) GetMissingSkills(ctx context.Context, domain string) ([]models.SkillRegistryEntry, error) {
	sql := `
		SELECT sr.skill_id, sr.skill_name, sr.missing_deps, sr.stale, sr.last_review, sr.auto_expand, sr.coverage
		FROM skill_registry sr
		WHERE array_length(sr.missing_deps, 1) > 0
	`
	args := []interface{}{}
	argIdx := 1

	if domain != "" {
		sql += fmt.Sprintf(` AND EXISTS (
			SELECT 1 FROM skills s
			WHERE s.id = sr.skill_id AND s.metadata->>'domain' = $%d
		)`, argIdx)
		args = append(args, domain)
		argIdx++
	}

	sql += " ORDER BY sr.coverage ASC, sr.skill_name"

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("get missing skills: %w", err)
	}
	defer rows.Close()

	var entries []models.SkillRegistryEntry
	for rows.Next() {
		var e models.SkillRegistryEntry
		if err := rows.Scan(&e.SkillID, &e.SkillName, &e.MissingDeps, &e.Stale, &e.LastReview, &e.AutoExpand, &e.Coverage); err != nil {
			return nil, fmt.Errorf("scan registry entry: %w", err)
		}
		entries = append(entries, e)
	}

	return entries, nil
}

// GetCoverage returns coverage statistics for a domain.
// Consolidated into a SINGLE query using conditional aggregation instead of
// 5 separate COUNT round-trips (performance: eliminates N+1 at the query level).
func (s *Store) GetCoverage(ctx context.Context, domain string) (map[string]interface{}, error) {
	var total, withDeps, withEvidence, missingCount int
	var avgCoverage float64

	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE EXISTS (
				SELECT 1 FROM skill_dependencies sd WHERE sd.skill_id = s.id
			)) AS with_deps,
			COUNT(*) FILTER (WHERE EXISTS (
				SELECT 1 FROM evidences e WHERE e.skill_id = s.id
			)) AS with_evidence,
			COALESCE(AVG(sr.coverage), 0.0) AS avg_coverage,
			COUNT(*) FILTER (WHERE array_length(sr.missing_deps, 1) > 0) AS missing_count
		FROM skills s
		LEFT JOIN skill_registry sr ON sr.skill_id = s.id
		WHERE ($1 = '' OR s.metadata->>'domain' = $1)
	`, domain).Scan(&total, &withDeps, &withEvidence, &avgCoverage, &missingCount)
	if err != nil {
		return nil, fmt.Errorf("coverage stats: %w", err)
	}

	coverage := 0.0
	if total > 0 {
		coverage = avgCoverage
	}

	return map[string]interface{}{
		"domain":               domain,
		"total_skills":         total,
		"skills_with_deps":     withDeps,
		"skills_with_evidence": withEvidence,
		"skills_missing_deps":  missingCount,
		"average_coverage":     coverage,
		"coverage_percentage":  fmt.Sprintf("%.1f%%", coverage*100),
	}, nil
}

// SubmitLearningJob creates a new learning job for project analysis.
func (s *Store) SubmitLearningJob(ctx context.Context, projectPath string, languages []string) (*models.LearningJob, error) {
	job := &models.LearningJob{
		ID:          uuid.New(),
		ProjectPath: projectPath,
		Status:      "pending",
		Languages:   languages,
	}

	// For now, just store in audit log. In production, this would insert into a learning_jobs table.
	details, _ := json.Marshal(map[string]interface{}{
		"project_path": projectPath,
		"languages":    languages,
		"job_id":       job.ID,
	})

	_, err := s.pool.Exec(ctx,
		"INSERT INTO audit_log (event, details) VALUES ($1, $2)",
		"learning_job_submitted", details,
	)
	if err != nil {
		return nil, fmt.Errorf("log learning job: %w", err)
	}

	return job, nil
}

// VectorSearch performs vector similarity search using pgvector.
func (s *Store) VectorSearch(ctx context.Context, embedding []float32, limit int) ([]models.SearchResult, error) {
	vec := pgvector.NewVector(embedding)
	// WHERE s.embedding IS NOT NULL (F2, code-review BLOCKING; aligned with the
	// reference sibling internal/db/vector.go's VectorSearch): skills.embedding is
	// a nullable column (migrations/001_initial.up.sql) that store.Create never
	// sets, so it is NULL in the ordinary partially-/un-populated state. On the
	// HNSW index-scan plan NULLs are skipped, but the cost-based planner CAN pick a
	// seqscan/top-N plan (small table, or no usable index) where `ORDER BY
	// s.embedding <=> $1 LIMIT $2` sorts NULL distances LAST and, once LIMIT
	// exceeds the non-NULL row count, RETURNS a NULL-embedding row with a NULL
	// score -- pgx v5 then errors "cannot scan NULL into *float64". Because
	// Store.Search deliberately does not mask a vector-leg error, that single NULL
	// row hard-fails EVERY hybrid Search and discards the trigram results. Filtering
	// NULLs in the vector leg makes correctness independent of the query plan.
	sql := `
		SELECT s.id, s.name, s.version, s.title, s.description, s.content,
		       s.metadata, s.status, s.kind, s.created_at, s.updated_at,
		       1 - (s.embedding <=> $1) as score
		FROM skills s
		WHERE s.embedding IS NOT NULL
		ORDER BY s.embedding <=> $1
		LIMIT $2
	`
	rows, err := s.pool.Query(ctx, sql, vec, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var results []models.SearchResult
	for rows.Next() {
		var r models.SearchResult
		var metadata []byte
		var description dbsql.NullString // F2/finding 7 precedent, see textSearch.
		if err := rows.Scan(
			&r.Skill.ID, &r.Skill.Name, &r.Skill.Version, &r.Skill.Title,
			&description, &r.Skill.Content, &metadata,
			&r.Skill.Status, &r.Skill.Kind, &r.Skill.CreatedAt, &r.Skill.UpdatedAt,
			&r.Score,
		); err != nil {
			return nil, fmt.Errorf("scan vector result: %w", err)
		}
		r.Skill.Description = description.String
		r.Skill.Metadata = metadata
		results = append(results, r)
	}
	// F4/finding 4: see textSearch's rows.Err() note above -- same mid-stream
	// truncation risk applies to this KNN query's row iteration.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search rows: %w", err)
	}

	return results, nil
}

// UpdateStatus changes the status of a skill by ID. Returns ErrSkillNotFound
// when the skill does not exist. The updated_at timestamp is refreshed
// automatically. An audit log entry is recorded for the status change.
//
// §G03 Validation pipeline promotion: used by the validation worker cycle to
// promote skills from draft → validated → active after passing all stages.
func (s *Store) UpdateStatus(ctx context.Context, skillID uuid.UUID, newStatus models.SkillStatus) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Fetch current status for the audit log.
		var oldStatus models.SkillStatus
		err := tx.QueryRow(ctx,
			`SELECT status FROM skills WHERE id = $1 FOR UPDATE`,
			skillID,
		).Scan(&oldStatus)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("%w: %s", ErrSkillNotFound, skillID)
			}
			return fmt.Errorf("fetch current status: %w", err)
		}

		// Update status + timestamp.
		tag, err := tx.Exec(ctx,
			`UPDATE skills SET status = $1, updated_at = NOW() WHERE id = $2`,
			newStatus, skillID,
		)
		if err != nil {
			return fmt.Errorf("update status: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: %s", ErrSkillNotFound, skillID)
		}

		// Audit log.
		if err := s.logAudit(ctx, tx, "status_change", &skillID, map[string]interface{}{
			"old_status": string(oldStatus),
			"new_status": string(newStatus),
		}); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		s.logger.Info("skill status updated",
			zap.String("skill_id", skillID.String()),
			zap.String("old_status", string(oldStatus)),
			zap.String("new_status", string(newStatus)),
		)

		return nil
	})
}

// ListSkills returns all skills with optional filtering.
func (s *Store) ListSkills(ctx context.Context, status models.SkillStatus, limit, offset int) ([]models.Skill, error) {
	sql := `
		SELECT id, name, version, title, description, content,
		       metadata, status, kind, created_at, updated_at
		FROM skills
	`
	args := []interface{}{}
	conditions := []string{}

	if status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, status)
	}

	if len(conditions) > 0 {
		sql += " WHERE " + strings.Join(conditions, " AND ")
	}

	sql += fmt.Sprintf(" ORDER BY name LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var skills []models.Skill
	for rows.Next() {
		var sk models.Skill
		var metadata []byte
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Version, &sk.Title, &sk.Description, &sk.Content, &metadata, &sk.Status, &sk.Kind, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		sk.Metadata = metadata
		skills = append(skills, sk)
	}

	return skills, nil
}

// logAudit is a helper for audit logging used by other skill package files.
func (s *Store) logAudit(ctx context.Context, tx pgx.Tx, event string, skillID *uuid.UUID, details map[string]interface{}) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (ts, event, skill_id, details)
		VALUES ($1, $2, $3, $4)
	`, time.Now().UTC(), event, skillID, detailsJSON)

	return err
}
