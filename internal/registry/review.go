// Package registry provides scheduled review jobs for skill health monitoring.
package registry

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// ReviewScheduler manages periodic skill review jobs.
type ReviewScheduler struct {
	registry *Registry
	cron     *cron.Cron
	running  bool
	mu       sync.Mutex
	cancel   context.CancelFunc
	ctx      context.Context
	wg       sync.WaitGroup
}

// StartReviewScheduler starts a background cron job that periodically checks
// all skills and marks stale ones. The interval controls how often the review runs.
// Common intervals: 1h (frequent), 24h (daily), 168h (weekly).
func (r *Registry) StartReviewScheduler(interval time.Duration) *ReviewScheduler {
	if interval < time.Minute {
		interval = time.Hour // Minimum 1 hour to avoid excessive DB load
	}

	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &ReviewScheduler{
		registry: r,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Use robfig/cron for more flexible scheduling
	// Map duration to a cron expression (run every N hours)
	hours := int(interval.Hours())
	if hours < 1 {
		hours = 1
	}

	cronExpr := fmt.Sprintf("0 */%d * * *", hours)
	if hours >= 24 {
		days := hours / 24
		if days == 1 {
			cronExpr = "0 0 * * *" // Daily at midnight
		} else {
			cronExpr = fmt.Sprintf("0 0 */%d * *", days)
		}
	}

	scheduler.cron = cron.New()
	_, err := scheduler.cron.AddFunc(cronExpr, func() {
		scheduler.performReview(ctx)
	})
	if err != nil {
		log.Printf("[ReviewScheduler] Failed to add cron job: %v", err)
		// Fall back to simple ticker-based scheduling
		scheduler.startTickerFallback(interval)
		return scheduler
	}

	scheduler.cron.Start()
	scheduler.running = true

	log.Printf("[ReviewScheduler] Started with cron expression: %s (interval: %v)", cronExpr, interval)

	// Run an immediate review on startup
	scheduler.wg.Add(1)
	go func() {
		defer scheduler.wg.Done()
		scheduler.performReview(ctx)
	}()

	return scheduler
}

// startTickerFallback uses a time.Ticker when cron scheduling fails.
func (rs *ReviewScheduler) startTickerFallback(interval time.Duration) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.running = true
	ticker := time.NewTicker(interval)

	rs.wg.Add(1)
	go func() {
		defer rs.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-rs.ctx.Done():
				log.Println("[ReviewScheduler] Ticker fallback stopped")
				return
			case <-ticker.C:
				rs.performReview(rs.ctx)
			}
		}
	}()

	// Run immediate review
	rs.wg.Add(1)
	go func() {
		defer rs.wg.Done()
		rs.performReview(rs.ctx)
	}()

	log.Printf("[ReviewScheduler] Fallback ticker started with interval: %v", interval)
}

// performReview checks all skills and marks those needing attention as
// stale. It delegates to Registry.RunReviewOnce, which marks skills stale
// on three health signals -- not reviewed in 30+ days, unresolved (missing)
// dependencies, and coverage below 25% -- and recalculates coverage and
// missing dependencies. See RunReviewOnce's own doc for the exact,
// order-sensitive sequence (the 30-day age mark must run before the
// coverage recalculation, per the G32 F1 remediation).
//
// G32 consolidation (research/p05_high_defect_fix_designs.md §4.3 step 2):
// this method previously duplicated all five steps inline via
// markOldSkillsStale/markMissingDepSkillsStale/markLowCoverageSkillsStale/
// UpdateCoverage/CalculateMissingDeps, while Registry.RunReviewOnce (below)
// implemented a SEPARATE, slightly-narrower 4-step version of the exact
// same review (missing the low-coverage stale-mark) -- an internal
// asymmetry with zero external callers on either side. RunReviewOnce is now
// the single, consolidated review implementation shared by this
// cron/ticker-fallback path AND internal/worker/runner.go's
// Runner.runRegistryReview ticker cycle. The markOldSkillsStale/
// markMissingDepSkillsStale/markLowCoverageSkillsStale helper methods below
// are intentionally left in place, unreferenced from here: per §4.3 step 5,
// removing ReviewScheduler's own scheduling wrapper is a SEPARATE §11.4.122
// operator keep-or-remove decision, not made by this change.
func (rs *ReviewScheduler) performReview(ctx context.Context) {
	start := time.Now()
	log.Println("[ReviewScheduler] Starting periodic review...")

	if err := rs.registry.RunReviewOnce(ctx); err != nil {
		log.Printf("[ReviewScheduler] Error running review: %v", err)
	}

	duration := time.Since(start)
	log.Printf("[ReviewScheduler] Review completed in %v", duration)
}

// markOldSkillsStale marks skills that haven't been reviewed in 30+ days as stale.
func (rs *ReviewScheduler) markOldSkillsStale(ctx context.Context) error {
	tag, err := rs.registry.pool.Inner().Exec(ctx, `
		UPDATE skill_registry sr
		SET stale = true
		WHERE sr.last_review < NOW() - INTERVAL '30 days'
		  AND EXISTS (
			  SELECT 1 FROM skills s
			  WHERE s.id = sr.skill_id
			    AND s.status IN ('validated', 'active')
		  )
		  AND sr.stale = false
	`)
	if err != nil {
		return fmt.Errorf("mark old skills stale: %w", err)
	}
	if tag.RowsAffected() > 0 {
		log.Printf("[ReviewScheduler] Marked %d skills stale (not reviewed in 30+ days)", tag.RowsAffected())
	}
	return nil
}

// markMissingDepSkillsStale marks skills with unresolved dependencies as stale.
func (rs *ReviewScheduler) markMissingDepSkillsStale(ctx context.Context) error {
	tag, err := rs.registry.pool.Inner().Exec(ctx, `
		UPDATE skill_registry sr
		SET stale = true
		WHERE sr.missing_deps != '{}'
		  AND sr.stale = false
	`)
	if err != nil {
		return fmt.Errorf("mark missing-dep skills stale: %w", err)
	}
	if tag.RowsAffected() > 0 {
		log.Printf("[ReviewScheduler] Marked %d skills stale (missing dependencies)", tag.RowsAffected())
	}
	return nil
}

// markLowCoverageSkillsStale marks skills with coverage below 25% as stale.
func (rs *ReviewScheduler) markLowCoverageSkillsStale(ctx context.Context) error {
	tag, err := rs.registry.pool.Inner().Exec(ctx, `
		UPDATE skill_registry sr
		SET stale = true
		WHERE sr.coverage < 0.25
		  AND EXISTS (
			  SELECT 1 FROM skills s
			  WHERE s.id = sr.skill_id
			    AND s.status IN ('validated', 'active')
		  )
		  AND sr.stale = false
	`)
	if err != nil {
		return fmt.Errorf("mark low-coverage skills stale: %w", err)
	}
	if tag.RowsAffected() > 0 {
		log.Printf("[ReviewScheduler] Marked %d skills stale (coverage < 25%%)", tag.RowsAffected())
	}
	return nil
}

// Stop gracefully shuts down the review scheduler.
func (rs *ReviewScheduler) Stop() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if !rs.running {
		return
	}

	log.Println("[ReviewScheduler] Stopping...")

	if rs.cancel != nil {
		rs.cancel()
	}

	if rs.cron != nil {
		stopCtx := rs.cron.Stop()
		<-stopCtx.Done()
	}

	// Wait for background goroutines to finish (with timeout)
	done := make(chan struct{})
	go func() {
		rs.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[ReviewScheduler] Stopped cleanly")
	case <-time.After(10 * time.Second):
		log.Println("[ReviewScheduler] Stop timed out, forcing shutdown")
	}

	rs.running = false
}

// IsRunning returns whether the scheduler is currently active.
func (rs *ReviewScheduler) IsRunning() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.running
}

// ---------------------------------------------------------------------------
// Convenience constructors
// ---------------------------------------------------------------------------

// NewDailyReviewScheduler starts a scheduler that runs reviews once per day.
func (r *Registry) NewDailyReviewScheduler() *ReviewScheduler {
	return r.StartReviewScheduler(24 * time.Hour)
}

// NewHourlyReviewScheduler starts a scheduler that runs reviews every hour.
func (r *Registry) NewHourlyReviewScheduler() *ReviewScheduler {
	return r.StartReviewScheduler(time.Hour)
}

// RunReviewOnce performs a single review cycle synchronously. It is the
// single, consolidated review implementation shared by both
// registry.ReviewScheduler's cron/ticker-fallback path (performReview,
// above) and internal/worker/runner.go's Runner.runRegistryReview ticker
// cycle (research/p05_high_defect_fix_designs.md §4.3) -- there is exactly
// one place these review checks are implemented, so the two ticks can
// never drift out of sync with each other again.
//
// It performs five checks, in this ORDER (the order is load-bearing, not
// incidental -- see the G32 F1 comment inside the body):
//  1. mark stale: not reviewed in 30+ days (reads last_review BEFORE it is
//     refreshed by step 4 -- MUST stay first)
//  2. recalculate coverage (also refreshes last_review = NOW())
//  3. recalculate missing dependencies
//  4. mark stale: unresolved (missing) dependencies (reads step-3 output)
//  5. mark stale: coverage below 25% (reads step-2 output)
func (r *Registry) RunReviewOnce(ctx context.Context) error {
	log.Println("[ReviewScheduler] Running one-off review...")
	start := time.Now()

	// Mark stale skills based on age -- this MUST run BEFORE UpdateCoverage.
	//
	// G32 F1 remediation (research/p05_high_defect_fix_designs.md §4):
	// UpdateCoverage (registry.go) sets last_review = NOW() for every
	// validated/active registry row. If this age check ran AFTER
	// UpdateCoverage (as the G32 consolidation originally placed it), the
	// "last_review < NOW() - INTERVAL '30 days'" predicate and
	// UpdateCoverage's WHERE clause select the SAME row-set, so every row
	// eligible for the age mark would have just had its last_review
	// refreshed to NOW() -- an unsatisfiable conjunction that made this
	// check provably DEAD within one cycle. Running it first (matching the
	// pre-consolidation performReview ordering) lets it read last_review
	// as-of the previous cycle. DO NOT reorder this below UpdateCoverage.
	//
	// The missing-dependency and low-coverage marks below deliberately stay
	// AFTER their recalcs (CalculateMissingDeps / UpdateCoverage) so they
	// read freshly-recomputed columns; moving THEM before the recalcs would
	// mark stale on stale, previous-cycle values -- a different bug.
	if _, err := r.pool.Exec(ctx, `
		UPDATE skill_registry sr
		SET stale = true
		WHERE (sr.last_review < NOW() - INTERVAL '30 days' OR sr.last_review IS NULL)
		  AND EXISTS (
			  SELECT 1 FROM skills s
			  WHERE s.id = sr.skill_id
			    AND s.status IN ('validated', 'active')
		  )
		  AND sr.stale = false
	`); err != nil {
		return fmt.Errorf("mark old skills stale: %w", err)
	}

	// Update coverage (recomputes coverage AND refreshes last_review = NOW()
	// for validated/active rows -- hence the age mark above must precede it).
	if err := r.UpdateCoverage(ctx); err != nil {
		return fmt.Errorf("update coverage: %w", err)
	}

	// Calculate missing deps
	if err := r.CalculateMissingDeps(ctx); err != nil {
		return fmt.Errorf("calculate missing deps: %w", err)
	}

	// Mark skills with missing deps as stale (reads the missing_deps just
	// recomputed by CalculateMissingDeps).
	if _, err := r.pool.Exec(ctx, `
		UPDATE skill_registry
		SET stale = true
		WHERE missing_deps != '{}' AND stale = false
	`); err != nil {
		return fmt.Errorf("mark missing-dep skills stale: %w", err)
	}

	// Mark skills with very low coverage (<25%) as stale (reads the coverage
	// just recomputed by UpdateCoverage). This step was previously missing
	// from RunReviewOnce -- performReview's own inline 5-step body had it
	// (markLowCoverageSkillsStale) but RunReviewOnce did not, an internal
	// asymmetry independently found while consolidating the two review paths
	// (research/p05_high_defect_fix_designs.md §4.3 step 2).
	if _, err := r.pool.Exec(ctx, `
		UPDATE skill_registry sr
		SET stale = true
		WHERE sr.coverage < 0.25
		  AND EXISTS (
			  SELECT 1 FROM skills s
			  WHERE s.id = sr.skill_id
			    AND s.status IN ('validated', 'active')
		  )
		  AND sr.stale = false
	`); err != nil {
		return fmt.Errorf("mark low-coverage skills stale: %w", err)
	}

	log.Printf("[ReviewScheduler] One-off review completed in %v", time.Since(start))
	return nil
}
