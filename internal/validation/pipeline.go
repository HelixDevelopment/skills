package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// juryMinApprovals is the fail-closed floor on real approvals required for the
// jury stage to reach consensus (§G05). An empty jury is a hard BLOCK; a
// configured threshold below this floor is raised to it.
const juryMinApprovals = 2

// stageOrder is the fixed reporting order of pipeline stages.
var stageOrder = []string{"source_verification", "static_code_check", "llm_jury", "cross_reference"}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ValidationResult captures the outcome of the full validation pipeline.
type ValidationResult struct {
	SkillID    uuid.UUID              `json:"skill_id"`
	Passed     bool                   `json:"passed"`
	Stage      string                 `json:"stage"`  // "all_stages" on pass, else the first non-pass stage
	Stages     map[string]StageStatus `json:"stages"` // per-stage verdict (PASS/FAIL/SKIP/BLOCKED/N/A)
	Details    map[string]string      `json:"details"`
	ApprovedBy int                    `json:"approved_by"` // number of jury models that approved
}

// JuryResult captures the multi-model consensus outcome.
type JuryResult struct {
	Votes     map[string]bool `json:"votes"`     // model name -> approved
	Consensus bool            `json:"consensus"` // true if threshold met
	Feedback  string          `json:"feedback"`  // aggregated feedback
}

// JuryMember represents a single LLM validator in the jury.
type JuryMember struct {
	Name   string
	Weight float64 // voting weight (default 1.0)
	LLM    LLMValidator
}

// LLMValidator is the interface for a single LLM validator.
type LLMValidator interface {
	// ValidateSkill evaluates a skill and returns an approval decision with feedback.
	ValidateSkill(ctx context.Context, skill *models.Skill) (approved bool, feedback string, err error)
}

// Pipeline validates skills through multiple independent, NON-EXECUTING layers
// to ensure accuracy and prevent hallucinated content from entering the
// knowledge graph. It executes no untrusted code (see the package doc).
type Pipeline struct {
	store  *skill.Store
	cfg    config.ValidationConfig
	logger *zap.Logger
	jury   []JuryMember
	// executor is the opt-in Tier B IsolatedExecutor (default
	// SkipIsolatedExecutor, always SKIP — see sandbox.go). It is intentionally
	// NOT invoked by Validate(): Validate's fail-closed 4-stage contract
	// (source_verification / static_code_check / llm_jury / cross_reference,
	// see the Validate doc comment below) treats any SKIP as fail-closed
	// (computeOverallVerdict), so wiring the default SkipIsolatedExecutor in as
	// a 5th aggregated stage would force every validation to FAIL even though
	// Tier B is meant to be an optional, off-by-default extension. There is
	// also no first-party "POC code + language" concept on models.Skill yet to
	// feed IsolatedExecutor.Execute with — that product surface does not exist.
	// This field is deliberate forward-wiring for a future concrete executor
	// (rootless-Podman, submodule-gated) and a future Tier-B stage that is
	// explicitly excluded from the pass/fail aggregate rather than silently
	// dead state; WithIsolatedExecutor lets callers/tests observe it directly
	// in the meantime.
	executor   IsolatedExecutor
	httpClient *http.Client // SSRF-guarded egress client
	// hostGuard screens an egress host; nil means use the default screenHost.
	// It is an injectable seam for hermetic tests, never a production override.
	hostGuard func(ctx context.Context, host string) error
}

// PipelineOption allows optional configuration.
type PipelineOption func(*Pipeline)

// WithJury sets custom jury members for LLM validation.
func WithJury(jury []JuryMember) PipelineOption {
	return func(p *Pipeline) {
		p.jury = jury
	}
}

// WithIsolatedExecutor sets a custom opt-in isolated executor (Tier B). The
// default is the fail-closed SkipIsolatedExecutor.
func WithIsolatedExecutor(e IsolatedExecutor) PipelineOption {
	return func(p *Pipeline) {
		p.executor = e
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// NewPipeline creates a new NON-EXECUTING validation pipeline. It builds no
// host-execution sandbox: the default executor is the fail-closed
// SkipIsolatedExecutor and the egress client is SSRF-guarded.
func NewPipeline(store *skill.Store, cfg config.ValidationConfig, logger *zap.Logger, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		store:      store,
		cfg:        cfg,
		logger:     logger,
		executor:   NewSkipIsolatedExecutor(),
		httpClient: newGuardedHTTPClient(30 * time.Second),
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.executor == nil {
		p.executor = NewSkipIsolatedExecutor()
	}
	if p.httpClient == nil {
		p.httpClient = newGuardedHTTPClient(30 * time.Second)
	}
	return p
}

// ---------------------------------------------------------------------------
// Full validation
// ---------------------------------------------------------------------------

// Validate performs full multi-layer, NON-EXECUTING validation on a skill:
//  1. Source verification (SSRF-guarded, fail-closed hashing)
//  2. Static code check (non-executing; runs no untrusted code)
//  3. LLM jury validation (fail-closed multi-model consensus)
//  4. Cross-reference consistency check
//
// The overall verdict is fail-closed: the skill passes only when every enabled
// stage produced a real positive verdict; a SKIP/FAIL/BLOCKED in any stage never
// upgrades to an overall PASS.
//
// The opt-in Tier B IsolatedExecutor (p.executor, see its field doc) is
// deliberately NOT one of these 4 stages and is NOT invoked here: it is
// forward-wiring for a not-yet-built concrete executor, and folding its
// default SKIP into this fail-closed aggregate would force every validation
// to FAIL. This is a documented, intentional gap, not silent dead state.
func (p *Pipeline) Validate(ctx context.Context, s *models.Skill) (*ValidationResult, error) {
	if p.store == nil {
		return nil, fmt.Errorf("validation pipeline: store is nil (fail-closed)")
	}

	p.logger.Info("starting validation pipeline",
		zap.String("skill", s.Name),
		zap.String("skill_id", s.ID.String()),
	)

	result := &ValidationResult{
		SkillID: s.ID,
		Details: make(map[string]string),
		Stages:  make(map[string]StageStatus),
	}

	// Stage 1: Source verification (SSRF-guarded, fail-closed).
	srcStatus, srcDetail := p.sourceVerifyStage(ctx, s)
	result.Stages["source_verification"] = srcStatus
	result.Details["source_verification"] = srcDetail

	// Stage 2: Static code check (NON-EXECUTING).
	codeStatus, codeDetail := p.staticCodeStage(s)
	result.Stages["static_code_check"] = codeStatus
	result.Details["static_code_check"] = codeDetail

	// Stage 3: LLM jury (fail-closed on empty jury).
	juryStatus, juryDetail, approvedBy := p.juryStage(ctx, s)
	result.Stages["llm_jury"] = juryStatus
	result.Details["llm_jury"] = juryDetail
	result.ApprovedBy = approvedBy

	// Stage 4: Cross-reference consistency.
	xrefStatus, xrefDetail := p.crossReferenceStage(ctx, s)
	result.Stages["cross_reference"] = xrefStatus
	result.Details["cross_reference"] = xrefDetail

	result.Passed = computeOverallVerdict(result.Stages)
	if result.Passed {
		result.Stage = "all_stages"
	} else {
		result.Stage = firstNonPassStage(result.Stages)
	}

	p.logValidationResult(ctx, s, result)
	p.logger.Info("validation complete",
		zap.String("skill", s.Name),
		zap.Bool("passed", result.Passed),
		zap.String("stage", result.Stage),
		zap.Int("jury_approvals", result.ApprovedBy),
	)
	return result, nil
}

// firstNonPassStage returns the first stage (in reporting order) whose verdict
// is not a PASS or N/A, for surfacing the blocking reason.
func firstNonPassStage(stages map[string]StageStatus) string {
	for _, name := range stageOrder {
		st, ok := stages[name]
		if !ok {
			continue
		}
		if st != StagePass && st != StageNA {
			return name
		}
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Stage 1: Source verification (fail-closed, SSRF-guarded)
// ---------------------------------------------------------------------------

// sourceVerifyStage verifies every attached resource, fail-closed.
func (p *Pipeline) sourceVerifyStage(ctx context.Context, s *models.Skill) (StageStatus, string) {
	if len(s.Resources) == 0 {
		return StageNA, "no resources to verify"
	}
	var errs []string
	verified := 0
	for i := range s.Resources {
		res := &s.Resources[i]
		if res.URL == "" {
			continue
		}
		if err := p.verifySingleResource(ctx, res); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", res.URL, err))
			continue
		}
		verified++
	}
	if len(errs) > 0 {
		return StageFail, "resource verification failed: " + strings.Join(errs, "; ")
	}
	if verified == 0 {
		return StageNA, "no verifiable resource URLs"
	}
	return StagePass, fmt.Sprintf("verified %d resource(s)", verified)
}

// SourceVerify checks that all resources attached to a skill are reachable and
// (for typed resources) that cached content hashes match. It returns a non-nil
// error when the source-verification stage does not pass.
func (p *Pipeline) SourceVerify(ctx context.Context, s *models.Skill) error {
	st, detail := p.sourceVerifyStage(ctx, s)
	if st == StagePass || st == StageNA {
		return nil
	}
	return fmt.Errorf("%s", detail)
}

// verifySingleResource verifies one resource, FAIL-CLOSED at every step:
//   - only http/https URLs are accepted;
//   - official-doc/code resources MUST carry a stored content hash;
//   - the egress host is SSRF-screened (and re-screened at dial time);
//   - any fetch/read error is a verification FAILURE (never a pass);
//   - a stored hash must match the freshly fetched content.
func (p *Pipeline) verifySingleResource(ctx context.Context, res *models.Resource) error {
	u, err := url.Parse(res.URL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q (only http/https)", u.Scheme)
	}

	// Fail-closed: official-doc/code resources MUST carry a stored content hash.
	if hashRequiredType(res.ResourceType) && strings.TrimSpace(res.FetchedHash) == "" {
		return fmt.Errorf("stored content hash required for %q resource but none present", res.ResourceType)
	}

	// SSRF guard (pre-flight; the guarded client re-checks at dial time).
	guard := p.hostGuard
	if guard == nil {
		guard = screenHost
	}
	if err := guard(ctx, u.Hostname()); err != nil {
		return err
	}

	// Fetch content; ANY fetch/read error is a verification FAILURE (fail-closed).
	content, err := p.fetchResource(ctx, res.URL)
	if err != nil {
		return fmt.Errorf("fetch failed (fail-closed): %w", err)
	}

	// If a stored hash exists, the fetched content MUST match it.
	if h := strings.TrimSpace(res.FetchedHash); h != "" {
		got := sha256Hex(content)
		if got != h {
			return fmt.Errorf("content hash mismatch (content changed): got %s want %s", got, h)
		}
	}
	return nil
}

// fetchResource performs a size-capped GET over the SSRF-guarded client.
func (p *Pipeline) fetchResource(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return nil, err
	}
	return content, nil
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Stage 2: Static code check (NON-EXECUTING)
// ---------------------------------------------------------------------------

// staticCodeStage runs the non-executing static code check. It NEVER executes
// any code; a parse note on an illustrative fragment is informational and does
// not fail the stage. The correctness oracle is the jury + source + cross-ref.
func (p *Pipeline) staticCodeStage(s *models.Skill) (StageStatus, string) {
	snippets := extractCodeBlocks(s.Content)
	if len(snippets) == 0 {
		return StageNA, "no code blocks to check"
	}
	rep := staticCheckCode(snippets)
	detail := fmt.Sprintf("non-executing static check: %d block(s), %d checked, %d unchecked (no in-process front-end)",
		rep.Total, rep.Checked, rep.Unchecked)
	if len(rep.Notes) > 0 {
		detail += "; notes: " + strings.Join(rep.Notes, " | ")
	}
	return StagePass, detail
}

// codeSnippet represents an extracted code block.
type codeSnippet struct {
	Code     string
	Language string
}

// extractCodeBlocks extracts fenced code blocks from markdown content.
func extractCodeBlocks(content string) []codeSnippet {
	var snippets []codeSnippet

	lines := strings.Split(content, "\n")
	var inBlock bool
	var currentBlock strings.Builder
	var currentLang string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inBlock {
				// End block
				snippets = append(snippets, codeSnippet{
					Code:     strings.TrimSpace(currentBlock.String()),
					Language: currentLang,
				})
				currentBlock.Reset()
				inBlock = false
				currentLang = ""
			} else {
				// Start block
				inBlock = true
				currentLang = strings.TrimSpace(trimmed[3:])
			}
			continue
		}

		if inBlock {
			currentBlock.WriteString(line)
			currentBlock.WriteString("\n")
		}
	}

	return snippets
}

// ---------------------------------------------------------------------------
// Stage 3: LLM Jury (fail-closed)
// ---------------------------------------------------------------------------

// juryThreshold returns the effective approval threshold, floored at
// juryMinApprovals so consensus always requires at least two real approvals.
func juryThreshold(cfg config.ValidationConfig) int {
	t := cfg.ApprovalThreshold
	if t < juryMinApprovals {
		t = juryMinApprovals
	}
	return t
}

// juryStage runs the jury and maps its result onto a stage verdict. An empty
// jury while validation is enabled is a hard BLOCK (never a pass).
func (p *Pipeline) juryStage(ctx context.Context, s *models.Skill) (StageStatus, string, int) {
	jr, err := p.LLMJury(ctx, s)
	if err != nil {
		return StageFail, "jury error: " + err.Error(), 0
	}
	approved := 0
	for _, v := range jr.Votes {
		if v {
			approved++
		}
	}
	if len(p.jury) == 0 {
		return StageBlocked, jr.Feedback, approved
	}
	if !jr.Consensus {
		return StageFail, fmt.Sprintf("insufficient consensus: %d approved (need >= %d of %d votes)",
			approved, juryThreshold(p.cfg), len(jr.Votes)), approved
	}
	return StagePass, fmt.Sprintf("consensus reached: %d approved", approved), approved
}

// LLMJury runs multi-model validation to ensure skills meet quality standards.
// FAIL-CLOSED (§G05): an empty jury while validation is enabled is a hard BLOCK
// (never an auto-pass), and consensus requires at least juryMinApprovals real
// approvals.
func (p *Pipeline) LLMJury(ctx context.Context, s *models.Skill) (*JuryResult, error) {
	if len(p.jury) == 0 {
		// FAIL-CLOSED: no jury configured is a hard BLOCK, never an auto-pass.
		p.logger.Warn("no jury members configured — BLOCKING (fail-closed)",
			zap.String("skill", s.Name),
		)
		return &JuryResult{
			Votes:     map[string]bool{},
			Consensus: false,
			Feedback:  "no jury configured — validation BLOCKED (fail-closed); a real multi-model jury (>= 2 approvals) is required",
		}, nil
	}

	p.logger.Info("running LLM jury",
		zap.String("skill", s.Name),
		zap.Int("jury_size", len(p.jury)),
		zap.Int("threshold", juryThreshold(p.cfg)),
	)

	votes := make(map[string]bool, len(p.jury))
	var feedbackParts []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	juryCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for _, member := range p.jury {
		wg.Add(1)
		go func(m JuryMember) {
			defer wg.Done()

			approved, feedback, err := m.LLM.ValidateSkill(juryCtx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				p.logger.Warn("juror failed", zap.String("juror", m.Name), zap.Error(err))
				votes[m.Name] = false
				feedbackParts = append(feedbackParts, fmt.Sprintf("%s: error - %v", m.Name, err))
				return
			}
			votes[m.Name] = approved
			feedbackParts = append(feedbackParts, fmt.Sprintf("%s: %s", m.Name, feedback))
		}(member)
	}

	wg.Wait()

	approvalCount := 0
	for _, approved := range votes {
		if approved {
			approvalCount++
		}
	}

	// Consensus requires the (floored) threshold AND at least juryMinApprovals
	// real votes to have been cast — never a single-juror or empty-jury pass.
	consensus := approvalCount >= juryThreshold(p.cfg) && len(votes) >= juryMinApprovals

	p.logger.Info("LLM jury complete",
		zap.String("skill", s.Name),
		zap.Int("approvals", approvalCount),
		zap.Int("required", juryThreshold(p.cfg)),
		zap.Bool("consensus", consensus),
	)

	return &JuryResult{
		Votes:     votes,
		Consensus: consensus,
		Feedback:  strings.Join(feedbackParts, "\n"),
	}, nil
}

// ---------------------------------------------------------------------------
// Stage 4: Cross-reference
// ---------------------------------------------------------------------------

// crossReferenceStage maps CrossReference onto a stage verdict.
func (p *Pipeline) crossReferenceStage(ctx context.Context, s *models.Skill) (StageStatus, string) {
	if err := p.CrossReference(ctx, s); err != nil {
		return StageFail, err.Error()
	}
	return StagePass, "passed"
}

// CrossReference checks consistency of a skill against existing skills in the
// knowledge graph. It verifies that referenced dependencies exist and that there
// are no naming contradictions. It executes no untrusted code.
//
// Both checks use an EXACT lookup (Store.GetByName), never the fuzzy hybrid
// Store.Search (Fable code-review remediation, finding 1, BLOCKING): Search's
// ranking is RRF-fused across a trigram leg and, once a query-side embedder is
// wired (§G29), a vector leg -- and an exact trigram match tops out at
// trigramRRFWeight/(rrfK+1) = 0.9/61 while ANY embedded row's rank-0 vector hit
// scores vectorRRFWeight/(rrfK+1) = 1.0/61, so a small-limit Search (limit=1
// for the dependency check, limit=5 for the conflict check) can rank an
// unrelated EMBEDDED skill above the exact-name match and, at limit=1, drop the
// exact match from the result set entirely. That would make CrossReference's
// existence/conflict verdict depend on which OTHER, unrelated skills happen to
// carry a populated embedding -- exactly the failure this fix closes.
// GetByName's `WHERE name = $1` lookup is exact and embedding-state-independent:
// its answer never changes based on what else in the graph has an embedding.
func (p *Pipeline) CrossReference(ctx context.Context, s *models.Skill) error {
	// Check that all dependencies exist.
	for _, dep := range s.Dependencies {
		if dep.DependsOn == uuid.Nil {
			return fmt.Errorf("dependency has empty ID for skill %q", dep.DependsOnName)
		}

		if _, err := p.store.GetByName(ctx, dep.DependsOnName); err != nil {
			if errors.Is(err, skill.ErrSkillNotFound) {
				return fmt.Errorf("dependency %q not found in knowledge graph", dep.DependsOnName)
			}
			return fmt.Errorf("lookup dependency %q: %w", dep.DependsOnName, err)
		}
	}

	// Check for naming conflicts: does ANOTHER skill already own this exact name?
	existing, err := p.store.GetByName(ctx, s.Name)
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return nil // no existing skill carries this exact name: no conflict
		}
		return fmt.Errorf("lookup for naming conflict %q: %w", s.Name, err)
	}
	if existing.ID != s.ID {
		return fmt.Errorf("naming conflict: skill %q already exists with different ID", s.Name)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// logValidationResult records the validation outcome in the audit log.
func (p *Pipeline) logValidationResult(ctx context.Context, s *models.Skill, result *ValidationResult) {
	details := map[string]interface{}{
		"skill_name":  s.Name,
		"skill_id":    s.ID.String(),
		"passed":      result.Passed,
		"stage":       result.Stage,
		"stages":      result.Stages,
		"details":     result.Details,
		"approved_by": result.ApprovedBy,
	}

	event := db.AuditEventSkillValidated
	if !result.Passed {
		event = "skill.validation_failed"
	}

	if err := db.LogEventWithDetails(ctx, p.store.Pool(), event, &s.ID, details); err != nil {
		p.logger.Warn("failed to log validation result", zap.Error(err))
	}
}
