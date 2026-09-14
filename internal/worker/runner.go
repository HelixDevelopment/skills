// Package worker provides background job execution for the HelixKnowledge
// skill graph system. It manages concurrent worker goroutines with proper
// lifecycle control, graceful shutdown, retry logic, and progress tracking.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/autoexpand"
	"github.com/HelixDevelopment/skills/internal/codeanalysis"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/HelixDevelopment/skills/internal/skillsource"
	"github.com/HelixDevelopment/skills/internal/validation"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Worker types and constants
// ---------------------------------------------------------------------------

// JobType identifies the category of background job.
type JobType string

const (
	JobTypeAutoExpand     JobType = "autoexpand"
	JobTypeValidate       JobType = "validate"
	JobTypeCodeAnalysis   JobType = "codeanalysis"
	JobTypeRegistryReview JobType = "registry_review"
	JobTypeBatchEmbed     JobType = "batch_embed"
	// JobTypeSourceRescan triggers a full rescan of a registered skill source
	// (GitHub repo, filesystem path, or URL) through the source-ingestion
	// pipeline: fetch -> parse -> map -> dedup -> import (G83).
	JobTypeSourceRescan JobType = "source_rescan"
)

// JobStatus represents the current state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// registryReviewer is the minimal seam the registry-review ticker cycle
// (runRegistryReview, below) needs: run one full review pass and report
// whether it succeeded. It is satisfied in production by
// *registry.Registry (registry.Registry.RunReviewOnce, wired in NewRunner)
// and, in unit tests, by a spy that never touches a real database
// (registryreview_unit_test.go) -- the interface exists so that seam can be
// exercised at the unit level per §11.4.27 (mocks/spies are permitted ONLY
// in unit tests).
type registryReviewer interface {
	RunReviewOnce(ctx context.Context) error
}

// autoExpander is the minimal seam the autoexpand job handler
// (handleAutoExpand, below) needs: run the full expansion pipeline for a
// skill (detect its gaps, draft new sub-skills via the LLM, persist them,
// and cross-reference them into the tree) and report the outcome. It is
// satisfied in production by *autoexpand.Pipeline (Pipeline.Run, wired in
// NewRunner) and, in unit tests, by a spy that never touches a real
// database or LLM provider (autoexpand_unit_test.go) -- mirrors
// registryReviewer, immediately above, for the identical reason (§11.4.27:
// mocks/spies are permitted ONLY in unit tests).
//
// G03 (research/... worker-loop wiring): prior to this seam, the
// `autoexpand` job type was dispatched by executeJob's switch (runner.go)
// to handleAutoExpand, but that handler only unmarshaled the payload and
// logged -- it never called into internal/autoexpand at all (the package
// had ZERO production callers anywhere in the codebase; confirmed by
// inspection and an empirical throwaway-DB run showing autoexpand.Pipeline
// was never constructed in cmd/worker/main.go or cmd/server/main.go). The
// "Actual expansion is done by the autoExpandWorker polling loop" comment
// the stub carried was aspirational, not true: runAutoExpandCycle (the
// ticker loop's cycle function) also never called into the autoexpand
// package. This interface + the Runner.autoexpand field close that gap for
// the job-queue dispatch path (the "worker job loop"), the same way
// `registry registryReviewer` already closes the analogous gap for the
// registry-review ticker cycle.
type autoExpander interface {
	Run(ctx context.Context, skillName string, maxDepth int) (*autoexpand.ExpansionResult, error)
}

// validator is the minimal seam the validate job handler (handleValidate,
// below) needs: run the full validation pipeline for a skill and report the
// outcome. It is satisfied in production by *validation.Pipeline (Validate,
// wired in NewRunner) and, in unit tests, by a spy that never touches a real
// database or remote resource (TODO: validate_unit_test.go) -- mirrors
// autoExpander for the identical reason (§11.4.27: mocks/spies are permitted
// ONLY in unit tests).
type validator interface {
	Validate(ctx context.Context, s *models.Skill) (*validation.ValidationResult, error)
}

// codeAnalyzer is the minimal seam the code-analysis job handler
// (handleCodeAnalysis, below) needs: scan a project directory and extract
// patterns, imports, and language statistics. It is satisfied in production
// by *codeanalysis.Analyzer (AnalyzeProject, wired in NewRunner) and, in unit
// tests, by a spy that never touches a real filesystem (TODO:
// codeanalysis_unit_test.go) -- mirrors autoExpander for the identical reason
// (§11.4.27: mocks/spies are permitted ONLY in unit tests).
type codeAnalyzer interface {
	AnalyzeProject(ctx context.Context, projectPath string) (*codeanalysis.AnalysisResult, error)
}

// sourceSyncer is the minimal seam the source-rescan job handler
// (handleSourceRescan, below) needs: run the full source sync pipeline for a
// registered skill source and report the outcome. It is satisfied in
// production by *skillsource.Orchestrator (SyncSource, wired in NewRunner)
// and, in unit tests, by a spy that never touches a real database or network
// (source_rescan_unit_test.go) -- mirrors autoExpander for the identical
// reason (section 11.4.27: mocks/spies are permitted ONLY in unit tests).
type sourceSyncer interface {
	SyncSource(ctx context.Context, sourceID uuid.UUID) (*skillsource.SyncResult, error)
}

// Runner manages background job execution for the skill graph system.
// It coordinates multiple worker goroutines, handles graceful shutdown,
// and persists job state to the database for API status endpoints.
type Runner struct {
	pool       *db.Pool
	store      *skill.Store
	cfg        config.Config
	logger     *zap.Logger
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.RWMutex
	running    bool
	jobChan    chan Job
	metrics    Metrics
	registry   registryReviewer
	// autoexpand is the seam handleAutoExpand dispatches through (G03,
	// see autoExpander above). It is non-nil in production (wired in
	// NewRunner via autoexpand.NewPipeline) even when no LLM provider is
	// configured -- autoexpand.Pipeline degrades to its own no-LLM minimal
	// draft fallback in that case (DraftSkill), it does not need a nil
	// Runner-level seam to express "no LLM configured".
	autoexpand autoExpander
	// validator is the seam handleValidate and runValidationCycle dispatch
	// through (G03). It is wired in NewRunner via validation.NewPipeline.
	// Unlike autoexpand, there is no degraded "no validation" fallback:
	// validation.Pipeline.Validate always produces a verdict for every one of
	// its four stages -- even with no resources, no code blocks, and no jury
	// members (hard BLOCK, not a silent pass, per its fail-closed contract).
	validator validator
	// codeAnalyzer is the seam handleCodeAnalysis dispatches through (G03).
	// It is wired in NewRunner via codeanalysis.NewAnalyzer. The analyzer
	// uses its own Config-led Language / AllowedRoot / ExcludePatterns, so
	// it requires no per-call arguments beyond the project path.
	codeAnalyzer codeAnalyzer
	// sourceSyncer is the seam handleSourceRescan dispatches through (G83).
	// It is wired in NewRunner via skillsource.NewOrchestrator. The
	// orchestrator coordinates the full source-ingestion pipeline
	// (fetch -> parse -> map -> dedup -> import) for a single registered
	// skill source identified by UUID.
	sourceSyncer sourceSyncer
	// restartBackoffBase is the initial delay before a panicked worker
	// goroutine is restarted by supervise (doubling up to a 30s cap). It is a
	// field (not a const) purely so tests can shrink it for deterministic,
	// fast restart assertions (§11.4.50); production uses time.Second
	// (NewRunner). A zero value falls back to time.Second in supervise.
	restartBackoffBase time.Duration
}

// Job represents a unit of background work.
type Job struct {
	ID       uuid.UUID       `json:"id"`
	Type     JobType         `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	Status   JobStatus       `json:"status"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	Created  time.Time       `json:"created"`
	Started  *time.Time      `json:"started,omitempty"`
	Finished *time.Time      `json:"finished,omitempty"`
	Retries  int             `json:"retries"`
}

// Metrics tracks worker performance statistics.
type Metrics struct {
	JobsProcessed int64         `json:"jobs_processed"`
	JobsFailed    int64         `json:"jobs_failed"`
	JobsRetried   int64         `json:"jobs_retried"`
	AvgDuration   time.Duration `json:"avg_duration"`
	TotalDuration time.Duration `json:"total_duration"`
	LastJobTime   time.Time     `json:"last_job_time"`
	// PanicsRecovered counts panics caught + recovered by the worker panic
	// firewall (supervise / recoverJob, G11). A recovered panic is a TRACKED
	// signal, never a swallowed error (§11.4.201): a climbing PanicsRecovered
	// while cycles stall is how a recovered-panic crash-loop is detected.
	PanicsRecovered int64 `json:"panics_recovered"`
	mu              sync.RWMutex
}

// recordPanic increments the recovered-panic counter (safe for concurrent use).
func (m *Metrics) recordPanic() {
	m.mu.Lock()
	m.PanicsRecovered++
	m.mu.Unlock()
}

// JobResult is returned after job execution.
type JobResult struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// NewRunner creates a new background job runner.
//
// The registry-review ticker cycle (registryReviewWorker/runRegistryReview,
// below) is wired here to the full review logic via a fresh
// *registry.Registry over the same pool (research/
// p05_high_defect_fix_designs.md §4.3 step 1) -- a trivial, dependency-free
// construction (registry.Registry{pool}) that introduces no second
// goroutine and no second scheduling cadence.
//
// The autoexpand job-queue path (executeJob's JobTypeAutoExpand case,
// handleAutoExpand) is wired here the same way (G03): a fresh
// *autoexpand.Pipeline over the same store + the configured LLM provider.
// "Is an LLM provider configured" is derived SOLELY from
// autoexpand.NewLLMClientFromConfig's own error return (mirrors the
// mcp.NewMCPServer embedder-wiring pattern in internal/mcp/server.go,
// §G29) -- there is no second, hand-maintained provider check here that
// could drift from the factory's fail-closed policy. This claim is now
// actually true end-to-end (F1, G03 fix-round-2): DraftSkill
// (autoexpand/pipeline.go) used to carry its OWN second check -- a
// p.llm.(*OpenAILLM) type assertion -- that silently rejected every
// provider this factory supports OTHER than "openai"/"local"/"helixllm"
// (in particular "anthropic"), which WAS a second, drifted-from-the-factory
// check; DraftSkill now routes through the LLMClient interface
// (generateSkillDraft, llm.go) instead, so NewLLMClientFromConfig really is
// the only provider-selection logic in this path. An unconfigured or
// invalid provider (e.g. cfg.AutoExpand.LLMProvider == "", which
// config.Validate does not itself reject) is NOT fatal to worker startup:
// the Pipeline is still constructed, and DraftSkill's own no-LLM minimal
// fallback (autoexpand/pipeline.go) takes over -- autoexpand jobs still run
// end-to-end, they just draft a lower-fidelity skill until a provider is
// configured. CURRENT behavior; the minimal-draft fallback is slated for
// removal per G20 (never-persist-a-placeholder, GAPS_AND_RISKS_REGISTER.md)
// -- this ticket lands only G20's type-assertion half, not the fallback
// removal.
func NewRunner(pool *db.Pool, store *skill.Store, cfg config.Config, logger *zap.Logger) *Runner {
	var aeOpts []autoexpand.PipelineOption
	if llmClient, err := autoexpand.NewLLMClientFromConfig(cfg.AutoExpand, logger); err == nil {
		aeOpts = append(aeOpts, autoexpand.WithLLMClient(llmClient))
		logger.Info("auto-expand: LLM provider configured for the worker job loop (§G03)",
			zap.String("llm_provider", cfg.AutoExpand.LLMProvider))
	} else {
		logger.Warn("auto-expand: no usable LLM provider configured; autoexpand jobs will draft "+
			"skills via the minimal no-LLM fallback until one is configured (§G03)", zap.Error(err))
	}

	// Wire the query-side embedder onto the shared skill Store so its Search
	// becomes a genuine hybrid (vector KNN + trigram) search, matching the
	// MCP server wiring pattern (mcp/server.go §G29). The embedder is also
	// passed to the autoexpand pipeline for gap-detection semantic recall.
	// "Is the provider configured" is derived SOLELY from
	// db.NewEmbedderFromConfig's own error return — no second, hand-maintained
	// per-provider check that could drift from the factory's policy.
	var aeEmbedder db.Embedder
	if store != nil {
		store.WithLogger(logger)
		if emb, err := db.NewEmbedderFromConfig(cfg.Embedding); err == nil {
			store.WithEmbedder(emb)
			aeEmbedder = emb
			logger.Info("worker: hybrid skill search wired (§G29)",
				zap.String("embedding_provider", cfg.Embedding.Provider))
		} else {
			logger.Debug("worker: no embedding provider configured, using keyword-only search (§G29)", zap.Error(err))
		}
	}

	v := validation.NewPipeline(store, cfg.Validation, logger)
	ca := codeanalysis.NewAnalyzer(cfg.CodeAnalysis, logger)

	// Wire the source-rescan orchestrator (G83): a fresh
	// *skillsource.Orchestrator over the same pool + store, reusing the
	// runner's logger. The orchestrator is a pure coordination layer that
	// delegates all I/O to injected interfaces, so constructing it here
	// introduces no new goroutine or scheduling cadence.
	srcStore := skillsource.NewStore(pool, logger)
	srcOrch := skillsource.NewOrchestrator(srcStore, store, logger)

	return &Runner{
		pool:               pool,
		store:              store,
		cfg:                cfg,
		logger:             logger,
		jobChan:            make(chan Job, 100),
		registry:           registry.NewRegistry(pool),
		autoexpand:         autoexpand.NewPipeline(store, aeEmbedder, cfg.AutoExpand, logger, aeOpts...),
		validator:          v,
		codeAnalyzer:       ca,
		sourceSyncer:       srcOrch,
		restartBackoffBase: time.Second,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins all background workers. It launches goroutines for:
//   - Auto-expansion pipeline (if enabled)
//   - Validation worker (if enabled)
//   - Registry review worker
//   - Job queue processor
func (r *Runner) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		r.logger.Warn("runner already started")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancelFunc = cancel
	r.running = true

	// Every worker loop is launched THROUGH supervise (G11), so an unrecovered
	// panic in any loop is caught, logged with its stack, counted, and the loop
	// restarted -- never a process-killing exit(2). supervise owns each
	// goroutine's wg.Done(), so the loop bodies no longer call it themselves.

	// Start job queue processor
	r.wg.Add(1)
	go r.supervise(ctx, "job_queue", r.processJobQueue)

	// Start auto-expand worker
	if r.cfg.AutoExpand.Enabled {
		r.wg.Add(1)
		go r.supervise(ctx, "auto_expand", r.autoExpandWorker)
	}

	// Start validation worker
	if r.cfg.Validation.Enabled {
		r.wg.Add(1)
		go r.supervise(ctx, "validation", r.validationWorker)
	}

	// Start registry review worker
	r.wg.Add(1)
	go r.supervise(ctx, "registry_review", r.registryReviewWorker)

	r.logger.Info("worker runner started",
		zap.Bool("auto_expand", r.cfg.AutoExpand.Enabled),
		zap.Bool("validation", r.cfg.Validation.Enabled),
	)
}

// Stop gracefully shuts down all workers. It signals cancellation and waits
// for all goroutines to finish, respecting the provided context timeout.
func (r *Runner) Stop(ctx context.Context) {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	if r.cancelFunc != nil {
		r.cancelFunc()
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Info("worker runner stopped gracefully")
	case <-ctx.Done():
		r.logger.Warn("worker runner stop timed out, some goroutines may still be running")
	}
}

// IsRunning reports whether the runner is currently active.
func (r *Runner) IsRunning() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.running
}

// ---------------------------------------------------------------------------
// Panic firewall (G11 -- research/g11_worker_design.md §2.2)
// ---------------------------------------------------------------------------
//
// The worker is a SEPARATE process from the server (cmd/worker/main.go) and,
// unlike the server, has no gin.Recovery() net. In Go an unrecovered panic in
// ANY goroutine terminates the WHOLE process (runtime exit(2)), so a single
// runtime error in any cycle would take down job processing, auto-expand,
// validation, and registry-review together and -- if the triggering condition
// persists -- systemd would crash-loop the worker. supervise + recoverJob are
// the two firewalls that make a panic a recovered, logged, counted signal
// instead of an outage. A recovered panic is NEVER swallowed silently
// (§11.4.201): it is logged with its full stack and counted in
// Metrics.PanicsRecovered so a crash-loop is observable.

// supervise runs a long-lived worker loop fn under a panic firewall. It owns
// the goroutine's wg.Done(). On a panic it logs the stack, counts it, and
// restarts fn with capped exponential backoff (restartBackoffBase..30s) until
// ctx is cancelled. A clean return from fn (the loop observed ctx.Done) ends
// supervision. Backoff bounds a persistently-panicking loop so it neither
// busy-loops nor hides the defect -- the PanicsRecovered metric + logged stack
// surface it for systematic debugging (§11.4.102).
func (r *Runner) supervise(ctx context.Context, name string, fn func(context.Context)) {
	defer r.wg.Done()

	base := r.restartBackoffBase
	if base <= 0 {
		base = time.Second
	}
	const maxBackoff = 30 * time.Second
	backoff := base

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !r.runGuarded(ctx, name, fn) {
			// fn returned normally => the loop observed ctx cancellation and
			// exited. Nothing to restart.
			return
		}

		// Panic path: back off (capped) then restart, unless ctx is done.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runGuarded invokes fn under a deferred recover(), returning true iff fn
// panicked (recovered here). This recover() is the load-bearing guard: without
// it, a panic in fn escapes the goroutine and kills the process (its §1.1
// mutation makes TestSupervise_RecoversPanicAndRestarts_WorkerSurvives die).
func (r *Runner) runGuarded(ctx context.Context, name string, fn func(context.Context)) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			panicked = true
			r.metrics.recordPanic()
			r.logger.Error("worker goroutine panic -- recovered, restarting",
				zap.String("worker", name),
				zap.Any("panic", p),
				zap.ByteString("stack", debug.Stack()),
			)
		}
	}()
	fn(ctx)
	return false
}

// recoverJob runs a job handler h under the per-job panic firewall. A panic in
// h is recovered, logged with its stack, counted, and converted into a failed
// JobResult that flows into the existing retry/recordFailure path
// (executeJobWithRetry) -- never a process death (§11.4.147: a crashed job is a
// recorded, retryable failure, not a lost worker). A non-panicking handler's
// result is passed through unchanged. The recover() is load-bearing: its §1.1
// mutation makes TestRecoverJob_HandlerPanic_RecordedNotFatal die.
func (r *Runner) recoverJob(job Job, h func() JobResult) (result JobResult) {
	defer func() {
		if p := recover(); p != nil {
			r.metrics.recordPanic()
			r.logger.Error("job handler panic -- recovered, job marked failed",
				zap.String("job_id", job.ID.String()),
				zap.String("type", string(job.Type)),
				zap.Any("panic", p),
				zap.ByteString("stack", debug.Stack()),
			)
			result = JobResult{Success: false, Error: fmt.Sprintf("handler panic: %v", p)}
		}
	}()
	return h()
}

// ---------------------------------------------------------------------------
// Job queue processor
// ---------------------------------------------------------------------------

// SubmitJob enqueues a new background job for processing.
func (r *Runner) SubmitJob(ctx context.Context, jobType JobType, payload json.RawMessage) (*Job, error) {
	job := Job{
		ID:      uuid.New(),
		Type:    jobType,
		Payload: payload,
		Status:  JobStatusPending,
		Created: time.Now().UTC(),
	}

	// Persist job to database for status tracking
	if err := r.persistJob(ctx, &job); err != nil {
		r.logger.Error("failed to persist job", zap.Error(err), zap.String("job_id", job.ID.String()))
	}

	select {
	case r.jobChan <- job:
		r.logger.Debug("job submitted", zap.String("job_id", job.ID.String()), zap.String("type", string(jobType)))
		return &job, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// processJobQueue processes jobs from the channel with retry support.
// Launched via supervise (G11), which owns the WaitGroup Done and the panic
// firewall for this loop.
func (r *Runner) processJobQueue(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("job queue processor shutting down")
			return
		case job := <-r.jobChan:
			r.executeJobWithRetry(ctx, job)
		}
	}
}

// executeJobWithRetry executes a job with exponential backoff on failure.
func (r *Runner) executeJobWithRetry(ctx context.Context, job Job) {
	maxRetries := 3
	baseDelay := time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := baseDelay * time.Duration(1<<uint(attempt-1))
			r.logger.Info("retrying job",
				zap.String("job_id", job.ID.String()),
				zap.Int("attempt", attempt),
				zap.Duration("delay", delay),
			)
			r.metrics.mu.Lock()
			r.metrics.JobsRetried++
			r.metrics.mu.Unlock()

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		result := r.executeJob(ctx, job)
		if result.Success {
			r.recordSuccess(job, result)
			return
		}

		if attempt < maxRetries {
			r.logger.Warn("job failed, will retry",
				zap.String("job_id", job.ID.String()),
				zap.Int("attempt", attempt+1),
				zap.String("error", result.Error),
			)
			job.Retries = attempt + 1
		}
	}

	r.recordFailure(job, fmt.Errorf("job failed after %d retries", maxRetries))
}

// executeJob dispatches the job to the appropriate handler.
func (r *Runner) executeJob(ctx context.Context, job Job) JobResult {
	start := time.Now().UTC()
	job.Status = JobStatusRunning
	job.Started = &start

	if err := r.persistJob(ctx, &job); err != nil {
		r.logger.Error("failed to update job status", zap.Error(err))
	}

	// Dispatch under the per-job panic firewall (G11): a panicking handler
	// becomes a recorded failed JobResult that flows into the existing
	// retry/recordFailure path (executeJobWithRetry), never a process death.
	result := r.recoverJob(job, func() JobResult {
		switch job.Type {
		case JobTypeAutoExpand:
			return r.handleAutoExpand(ctx, job)
		case JobTypeValidate:
			return r.handleValidate(ctx, job)
		case JobTypeCodeAnalysis:
			return r.handleCodeAnalysis(ctx, job)
		case JobTypeRegistryReview:
			return r.handleRegistryReview(ctx, job)
		case JobTypeBatchEmbed:
			return r.handleBatchEmbed(ctx, job)
		case JobTypeSourceRescan:
			return r.handleSourceRescan(ctx, job)
		default:
			return JobResult{Success: false, Error: fmt.Sprintf("unknown job type: %s", job.Type)}
		}
	})

	duration := time.Since(start)
	r.metrics.mu.Lock()
	r.metrics.TotalDuration += duration
	r.metrics.JobsProcessed++
	r.metrics.LastJobTime = time.Now().UTC()
	if r.metrics.JobsProcessed > 0 {
		r.metrics.AvgDuration = r.metrics.TotalDuration / time.Duration(r.metrics.JobsProcessed)
	}
	r.metrics.mu.Unlock()

	return result
}

// ---------------------------------------------------------------------------
// Job handlers (stub implementations - real logic is in sub-packages)
// ---------------------------------------------------------------------------

// handleAutoExpand dequeues an autoexpand job and invokes the auto-growth
// pipeline end-to-end (G03): detect gaps for the requested skill, draft new
// sub-skills via the configured LLM provider (or the minimal no-LLM
// fallback), persist them, and cross-reference them into the tree
// (autoexpand.Pipeline.Run, dispatched through the autoExpander seam so this
// handler stays unit-testable per the registryReviewer pattern above).
func (r *Runner) handleAutoExpand(ctx context.Context, job Job) JobResult {
	var payload struct {
		SkillName string `json:"skill_name"`
		MaxDepth  int    `json:"max_depth"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("unmarshal payload: %v", err)}
	}

	if strings.TrimSpace(payload.SkillName) == "" {
		return JobResult{Success: false, Error: "skill_name is required"}
	}

	maxDepth := payload.MaxDepth
	if maxDepth <= 0 {
		maxDepth = r.cfg.AutoExpand.MaxDepth
	}

	r.logger.Info("auto-expand job", zap.String("skill", payload.SkillName), zap.Int("depth", maxDepth))

	if r.autoexpand == nil {
		return JobResult{Success: false, Error: "auto-expand pipeline not configured"}
	}

	expResult, err := r.autoexpand.Run(ctx, payload.SkillName, maxDepth)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("autoexpand run: %v", err)}
	}

	data, err := json.Marshal(expResult)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("marshal expansion result: %v", err)}
	}

	return JobResult{Success: true, Data: data}
}

func (r *Runner) handleValidate(ctx context.Context, job Job) JobResult {
	var payload struct {
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("unmarshal payload: %v", err)}
	}

	if strings.TrimSpace(payload.SkillName) == "" {
		return JobResult{Success: false, Error: "skill_name is required"}
	}

	r.logger.Info("validation job", zap.String("skill", payload.SkillName))

	if r.validator == nil {
		return JobResult{Success: false, Error: "validation pipeline not configured"}
	}

	skill, err := r.store.GetByName(ctx, payload.SkillName)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("lookup skill: %v", err)}
	}

	vResult, err := r.validator.Validate(ctx, skill)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("validation: %v", err)}
	}

	data, err := json.Marshal(vResult)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("marshal validation result: %v", err)}
	}

	return JobResult{Success: true, Data: data}
}

func (r *Runner) handleCodeAnalysis(ctx context.Context, job Job) JobResult {
	var payload struct {
		ProjectPath string `json:"project_path"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("unmarshal payload: %v", err)}
	}

	if strings.TrimSpace(payload.ProjectPath) == "" {
		return JobResult{Success: false, Error: "project_path is required"}
	}

	r.logger.Info("code analysis job", zap.String("project", payload.ProjectPath))

	if r.codeAnalyzer == nil {
		return JobResult{Success: false, Error: "code analyzer not configured"}
	}

	result, err := r.codeAnalyzer.AnalyzeProject(ctx, payload.ProjectPath)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("code analysis: %v", err)}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("marshal analysis result: %v", err)}
	}

	return JobResult{Success: true, Data: data}
}

func (r *Runner) handleRegistryReview(ctx context.Context, job Job) JobResult {
	r.logger.Info("registry review job")
	return JobResult{Success: true}
}

// handleBatchEmbed processes a batch embedding job. It generates and stores
// embeddings for all skills that don't yet have one, using the configured
// embedding provider with rate limiting.
//
// §11.4.85 Batch embedding worker integration.
func (r *Runner) handleBatchEmbed(ctx context.Context, job Job) JobResult {
	r.logger.Info("batch embedding job started")

	// Create the embedding provider from config.
	embedder, err := db.NewEmbedderFromConfig(r.cfg.Embedding)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("create embedder: %v", err)}
	}

	// Configure batch embedding with sensible defaults.
	batchCfg := db.BatchEmbedConfig{
		BatchSize:         100,
		RequestsPerSecond: 10,
		OnProgress: func(succeeded, failed, total int) {
			r.logger.Info("batch embedding progress",
				zap.Int("succeeded", succeeded),
				zap.Int("failed", failed),
				zap.Int("total", total))
		},
	}

	// Run batch embedding for all skills without embeddings.
	if err := db.BatchEmbedAllSkills(ctx, r.pool, embedder, batchCfg); err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("batch embed: %v", err)}
	}

	r.logger.Info("batch embedding job completed")
	return JobResult{Success: true}
}

// handleSourceRescan triggers a full rescan of a registered skill source
// through the source-ingestion pipeline (G83): fetch -> parse -> map -> dedup
// -> import. The source is identified by a UUID in the job payload. The
// handler dispatches through the sourceSyncer seam so it stays unit-testable
// per the autoExpander/registryReviewer pattern (section 11.4.27).
func (r *Runner) handleSourceRescan(ctx context.Context, job Job) JobResult {
	var payload struct {
		SourceID string `json:"source_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("unmarshal payload: %v", err)}
	}

	if strings.TrimSpace(payload.SourceID) == "" {
		return JobResult{Success: false, Error: "source_id is required"}
	}

	sourceID, err := uuid.Parse(payload.SourceID)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("invalid source_id: %v", err)}
	}

	r.logger.Info("source rescan job", zap.String("source_id", sourceID.String()))

	if r.sourceSyncer == nil {
		return JobResult{Success: false, Error: "source sync orchestrator not configured"}
	}

	syncResult, err := r.sourceSyncer.SyncSource(ctx, sourceID)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("source sync: %v", err)}
	}

	data, err := json.Marshal(syncResult)
	if err != nil {
		return JobResult{Success: false, Error: fmt.Sprintf("marshal sync result: %v", err)}
	}

	return JobResult{Success: true, Data: data}
}

// ---------------------------------------------------------------------------
// Worker loops
// ---------------------------------------------------------------------------

// autoExpandWorker periodically scans for skills that need expansion.
// Launched via supervise (G11), which owns the WaitGroup Done and the panic
// firewall for this loop.
func (r *Runner) autoExpandWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run immediately on start
	r.runAutoExpandCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("auto-expand worker shutting down")
			return
		case <-ticker.C:
			r.runAutoExpandCycle(ctx)
		}
	}
}

// validationWorker periodically picks up skills pending validation.
// Launched via supervise (G11), which owns the WaitGroup Done and the panic
// firewall for this loop.
func (r *Runner) validationWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("validation worker shutting down")
			return
		case <-ticker.C:
			r.runValidationCycle(ctx)
		}
	}
}

// registryReviewWorker periodically reviews skill registry health.
// Launched via supervise (G11), which owns the WaitGroup Done and the panic
// firewall for this loop.
func (r *Runner) registryReviewWorker(ctx context.Context) {
	interval := time.Duration(r.cfg.Registry.ReviewIntervalHours) * time.Hour
	if interval < time.Minute {
		interval = time.Minute // minimum 1 minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("registry review worker shutting down")
			return
		case <-ticker.C:
			r.runRegistryReview(ctx)
		}
	}
}

// ---------------------------------------------------------------------------
// Work cycles (called by worker loops)
// ---------------------------------------------------------------------------

func (r *Runner) runAutoExpandCycle(ctx context.Context) {
	// Find skills with missing dependencies (gaps)
	entries, err := r.store.GetMissingSkills(ctx, "")
	if err != nil {
		r.logger.Error("auto-expand: failed to get missing skills", zap.Error(err))
		return
	}

	if len(entries) == 0 {
		r.logger.Debug("auto-expand: no gaps found")
		return
	}

	r.logger.Info("auto-expand: found gaps",
		zap.Int("count", len(entries)),
		zap.Int("max_per_run", r.cfg.AutoExpand.MaxNewSkillsPerRun),
	)

	if r.autoexpand == nil {
		r.logger.Warn("auto-expand: pipeline not configured, skipping cycle")
		return
	}

	processed := 0
	for _, entry := range entries {
		if processed >= r.cfg.AutoExpand.MaxNewSkillsPerRun {
			break
		}

		if !entry.AutoExpand {
			continue
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		r.logger.Info("auto-expand: processing skill",
			zap.String("skill", entry.SkillName),
			zap.Strings("missing_deps", entry.MissingDeps),
		)

		maxDepth := r.cfg.AutoExpand.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 3
		}

		result, err := r.autoexpand.Run(ctx, entry.SkillName, maxDepth)
		if err != nil {
			r.logger.Error("auto-expand: expansion failed",
				zap.String("skill", entry.SkillName),
				zap.Error(err),
			)
			continue
		}

		r.logger.Info("auto-expand: expansion completed",
			zap.String("skill", entry.SkillName),
			zap.Int("skills_created", result.SkillsCreated),
			zap.Int("skills_updated", result.SkillsUpdated),
			zap.Strings("errors", result.Errors),
		)

		processed++
	}
}

func (r *Runner) runValidationCycle(ctx context.Context) {
	// Find skills in draft status that need validation
	skills, err := r.store.ListSkills(ctx, models.SkillStatusDraft, 10, 0)
	if err != nil {
		r.logger.Error("validation: failed to list draft skills", zap.Error(err))
		return
	}

	if len(skills) == 0 {
		return
	}

	if r.validator == nil {
		r.logger.Warn("validation: pipeline not configured, skipping cycle")
		return
	}

	for _, sk := range skills {
		select {
		case <-ctx.Done():
			return
		default:
		}

		r.logger.Info("validation: processing skill",
			zap.String("skill", sk.Name),
			zap.String("status", string(sk.Status)),
		)

		vResult, err := r.validator.Validate(ctx, &sk)
		if err != nil {
			r.logger.Error("validation: skill validation failed",
				zap.String("skill", sk.Name),
				zap.Error(err),
			)
			continue
		}

		if vResult.Passed {
			r.logger.Info("validation: skill passed all stages",
				zap.String("skill", sk.Name),
				zap.Int("jury_approvals", vResult.ApprovedBy),
			)
			// Promote skill to active status after passing all validation stages.
			if err := r.store.UpdateStatus(ctx, sk.ID, models.SkillStatusActive); err != nil {
				r.logger.Error("validation: failed to promote skill",
					zap.String("skill", sk.Name),
					zap.Error(err),
				)
			} else {
				r.logger.Info("validation: skill promoted to active",
					zap.String("skill", sk.Name),
				)
			}
		} else {
			r.logger.Warn("validation: skill failed validation",
				zap.String("skill", sk.Name),
				zap.String("stage", vResult.Stage),
			)
		}
	}
}

// runRegistryReview is the registry-review ticker cycle: it runs the full
// review pass (mark stale skills, recalculate coverage, recalculate missing
// dependencies) via the encapsulated review logic, registryReviewer.
// RunReviewOnce.
//
// G32 remediation (research/p05_high_defect_fix_designs.md §4): this cycle
// previously called ONLY r.store.GetCoverage(ctx, "") -- a read-only report
// that never marked anything stale, never recalculated coverage, and never
// recalculated missing dependencies -- while the full review logic
// (registry.Registry.RunReviewOnce, reachable via the ReviewScheduler
// flagship mechanism) had zero callers anywhere in the codebase. Rather
// than start a second, independently-scheduled goroutine (which would race
// this ticker against the same skill_registry rows -- the exact clash the
// design doc rejects at §4.2), this already-wired, already-config-driven
// ticker is retargeted to call the single, consolidated review
// implementation directly, making it the sole owner of the review cycle.
func (r *Runner) runRegistryReview(ctx context.Context) {
	if err := r.registry.RunReviewOnce(ctx); err != nil {
		r.logger.Error("registry review: run review once failed", zap.Error(err))
		return
	}

	r.logger.Info("registry review completed")

	// Audit log
	if err := db.LogEvent(ctx, r.pool, db.AuditEventExpansionStarted, nil, nil); err != nil {
		r.logger.Error("failed to log registry review", zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Job persistence (for API status endpoints)
// ---------------------------------------------------------------------------

// persistJob stores job state in the database for external status queries.
func (r *Runner) persistJob(ctx context.Context, job *Job) error {
	// Use audit log as a lightweight job tracking mechanism.
	// In production, this would insert into a dedicated jobs table.
	details, err := json.Marshal(map[string]interface{}{
		"job_id":   job.ID.String(),
		"type":     string(job.Type),
		"status":   string(job.Status),
		"retries":  job.Retries,
		"error":    job.Error,
		"result":   job.Result,
		"created":  job.Created,
		"started":  job.Started,
		"finished": job.Finished,
	})
	if err != nil {
		return fmt.Errorf("marshal job details: %w", err)
	}

	var event string
	switch job.Status {
	case JobStatusPending:
		event = "job.pending"
	case JobStatusRunning:
		event = "job.running"
	case JobStatusCompleted:
		event = "job.completed"
	case JobStatusFailed:
		event = "job.failed"
	case JobStatusCancelled:
		event = "job.cancelled"
	default:
		event = "job.unknown"
	}

	return db.LogEvent(ctx, r.pool, event, nil, details)
}

// recordSuccess updates metrics and persists a successful job result.
func (r *Runner) recordSuccess(job Job, result JobResult) {
	now := time.Now().UTC()
	job.Status = JobStatusCompleted
	job.Result = result.Data
	job.Finished = &now

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.persistJob(ctx, &job); err != nil {
		r.logger.Error("failed to persist completed job", zap.Error(err))
	}

	r.logger.Info("job completed",
		zap.String("job_id", job.ID.String()),
		zap.String("type", string(job.Type)),
	)
}

// recordFailure updates metrics and persists a failed job result.
func (r *Runner) recordFailure(job Job, execErr error) {
	now := time.Now().UTC()
	job.Status = JobStatusFailed
	job.Error = execErr.Error()
	job.Finished = &now

	r.metrics.mu.Lock()
	r.metrics.JobsFailed++
	r.metrics.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.persistJob(ctx, &job); err != nil {
		r.logger.Error("failed to persist failed job", zap.Error(err))
	}

	r.logger.Error("job failed",
		zap.String("job_id", job.ID.String()),
		zap.String("type", string(job.Type)),
		zap.Error(execErr),
	)
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// GetMetrics returns current worker metrics (safe for concurrent use).
func (r *Runner) GetMetrics() Metrics {
	r.metrics.mu.RLock()
	defer r.metrics.mu.RUnlock()
	return Metrics{
		JobsProcessed:   r.metrics.JobsProcessed,
		JobsFailed:      r.metrics.JobsFailed,
		JobsRetried:     r.metrics.JobsRetried,
		AvgDuration:     r.metrics.AvgDuration,
		TotalDuration:   r.metrics.TotalDuration,
		LastJobTime:     r.metrics.LastJobTime,
		PanicsRecovered: r.metrics.PanicsRecovered,
	}
}
