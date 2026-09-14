package worker

// G03 remediation (the autoexpand+worker half) -- live-database integration
// proof that an autoexpand job driven through the REAL worker + REAL job
// queue (Runner.SubmitJob -> the real r.jobChan -> Runner.executeJobWithRetry
// -> Runner.executeJob -> Runner.handleAutoExpand) genuinely dispatches into
// the REAL internal/autoexpand.Pipeline (constructed by the REAL NewRunner,
// exactly as cmd/worker/main.go constructs it) -- not the pre-fix stub that
// only unmarshaled the payload and logged.
//
// Scoping the LLM boundary: this suite does NOT require ANTHROPIC_API_KEY.
// That is a deliberate, evidence-based choice, not an omission -- confirmed
// empirically (throwaway-DB run, §11.4.6/§11.4.199) BEFORE writing this
// test: Pipeline.Run's gap-detection (DetectGapsForSkill ->
// collectGapsFromTree / detectGapsForSingleSkill, keyed on
// `dep.DependsOn == uuid.Nil`) can structurally never find a gap for ANY
// graph the store API constructs, because skill_dependencies.depends_on is
// a NOT-nullable foreign key (REFERENCES skills(id) ON DELETE CASCADE,
// migrations/001_initial.up.sql) and Store.GetByName's dependency query is
// an INNER JOIN against skills -- so every populated
// models.SkillDependency.DependsOn is, by construction, a real, existing
// skill id. Postgres itself refuses to INSERT a skill_dependencies row
// whose depends_on does not resolve to an existing skill THROUGH THIS
// PACKAGE'S OWN store-API insert path -- so a genuinely non-resolving edge
// cannot be seeded that way. (A raw-SQL zero-UUID skills row is a separate,
// schema-valid seeding path that DOES make the gate reachable -- see G137,
// GAPS_AND_RISKS_REGISTER.md; it is out of scope for this suite, which
// drives the real worker+queue+pipeline wiring, not the store's own
// insert-time guards.) Consequently Pipeline.Run's LLM-drafting branch
// (DraftSkill -> the configured LLMClient) is NEVER reached for this
// scenario regardless of wiring, and asserting SkillsCreated > 0 here would
// be dishonest (asserting something the current, unmodified gap-detection
// code cannot produce). That deeper gap-detection/registry-semantics
// mismatch is a real, separate, out-of-scope finding (reported alongside
// this change) -- distinct from "does the worker invoke the pipeline at
// all", which is what this suite proves.
//
// The G03 cross-reference addition this change makes to
// autoexpand.Pipeline (draftPersistAndCrossReference, pipeline.go) IS
// covered end-to-end with a REAL LLM + REAL database, in its own package:
// internal/autoexpand/pipeline_crossreference_integration_test.go (env-gated
// on ANTHROPIC_API_KEY, following llm_anthropic_integration_test.go's
// EXACT existing convention for that package's LLM boundary). Testing it
// there -- rather than forcing it through Pipeline.Run's currently-inert
// top-level scan -- is the only way to exercise the real create+
// cross-reference code path honestly.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST (§11.4.3): absent a configured live
// database this honestly t.Skip()s, never a fake PASS (§11.4.27).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/autoexpand"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

func TestHandleAutoExpand_DispatchesThroughRealWorkerAndJobQueue_RequiresLiveDatabase(t *testing.T) {
	admin, ok := workerSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := workerCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, workerRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	store := skill.NewStore(pool)

	sk := &models.Skill{
		Name:    "g03w.autoexpand.dispatch-target",
		Title:   "G03 worker-wiring dispatch target",
		Content: "content for the G03 autoexpand+worker integration test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create seed skill: %v", err)
	}

	// No LLM provider configured (LLMProvider left empty): NewRunner must
	// still construct a working *autoexpand.Pipeline (degrading to the
	// no-LLM minimal-draft fallback), never leave Runner.autoexpand nil.
	cfg := config.Config{
		AutoExpand: config.AutoExpandConfig{
			Enabled:            true,
			MaxDepth:           2,
			MaxNewSkillsPerRun: 5,
		},
	}
	runner := NewRunner(pool, store, cfg, zap.NewNop())
	if runner.autoexpand == nil {
		t.Fatal("NewRunner left Runner.autoexpand nil even with no LLM provider configured; " +
			"the pipeline must always be constructed (it degrades to its own no-LLM fallback)")
	}

	payload, err := json.Marshal(map[string]interface{}{
		"skill_name": sk.Name,
		"max_depth":  2,
	})
	if err != nil {
		t.Fatalf("marshal job payload: %v", err)
	}

	// Drive the REAL SubmitJob -> REAL channel -> REAL executeJobWithRetry
	// path, deterministically (no background supervise goroutine / ticker
	// wait, mirroring registryreview_integration_test.go's direct-call
	// convention for the SAME determinism reason, §11.4.50/§11.4.199: this
	// invokes the production functions the real dispatch path calls, not a
	// reimplementation of them).
	submitted, err := runner.SubmitJob(ctx, JobTypeAutoExpand, payload)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	var dequeued Job
	select {
	case dequeued = <-runner.jobChan:
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitJob did not enqueue the job onto the real job channel within 5s")
	}
	if dequeued.ID != submitted.ID {
		t.Fatalf("dequeued job id = %s, want %s (the exact job SubmitJob enqueued)", dequeued.ID, submitted.ID)
	}

	runner.executeJobWithRetry(ctx, dequeued)

	// Runtime-signature verification (§11.4.108): read back the REAL
	// audit_log row executeJob's persistJob wrote for THIS job's completion,
	// and confirm its embedded Result is a genuine
	// *autoexpand.ExpansionResult -- carrying the PIPELINE's OWN internally
	// minted job_id (Pipeline.Run mints jobID := uuid.New() itself,
	// pipeline.go) -- which the pre-fix stub's `{"skill":"<name>"}` payload
	// never had. This is only observable if handleAutoExpand really called
	// through to autoexpand.Pipeline.Run and not a stub.
	var detailsJSON []byte
	q := `
		SELECT details FROM audit_log
		WHERE event = 'job.completed' AND details->>'job_id' = $1
		ORDER BY ts DESC LIMIT 1
	`
	if err := pool.QueryRow(ctx, q, submitted.ID.String()).Scan(&detailsJSON); err != nil {
		t.Fatalf("query persisted job.completed audit row: %v (job never recorded completed -- "+
			"the dispatch through executeJobWithRetry -> executeJob -> handleAutoExpand did not "+
			"reach recordSuccess)", err)
	}

	var details struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(detailsJSON, &details); err != nil {
		t.Fatalf("unmarshal persisted job details: %v", err)
	}
	if details.Status != "completed" {
		t.Fatalf("persisted job status = %q, want %q", details.Status, "completed")
	}

	var expResult autoexpand.ExpansionResult
	if err := json.Unmarshal(details.Result, &expResult); err != nil {
		t.Fatalf("persisted job.result is not a valid autoexpand.ExpansionResult: %v (raw=%s) -- "+
			"this is the exact shape the pre-fix stub's `{\"skill\":\"<name>\"}` payload never had",
			err, details.Result)
	}
	if expResult.JobID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("persisted ExpansionResult.JobID is the zero UUID; Pipeline.Run must mint a real " +
			"job id (uuid.New()) for every run")
	}
	if expResult.JobID == submitted.ID {
		t.Fatal("persisted ExpansionResult.JobID equals the WORKER's job id; Pipeline.Run mints " +
			"its OWN distinct job id (pipeline.go: jobID := uuid.New()) -- their equality here would " +
			"mean this assertion is not actually distinguishing the real pipeline from a stub that " +
			"happened to echo the worker's job id back")
	}

	// Honest, evidence-grounded expectation for THIS seeded scenario (see
	// the file header): SkillsCreated is 0 because the seeded skill has no
	// dependency whose skill_dependencies row is unresolved -- which is
	// structurally impossible to construct given the FK-enforced schema.
	// A nonzero SkillsCreated here would indicate detection logic changed
	// out from under this test and should be investigated, not silently
	// accepted.
	if expResult.SkillsCreated != 0 {
		t.Errorf("SkillsCreated = %d, want 0 for this scenario (see file header: real "+
			"gap-detection cannot fire against a store-API-constructed graph with no "+
			"unresolved dependency reference)", expResult.SkillsCreated)
	}
}
