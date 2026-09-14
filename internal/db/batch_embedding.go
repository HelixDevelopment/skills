package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// EmbeddingProgress is called after each batch completes to report progress.
// succeeded is the cumulative number of skills embedded so far; failed is
// the cumulative number of failures; total is the total number of skills
// requested.
type EmbeddingProgress func(succeeded, failed, total int)

// BatchEmbedConfig controls batch-embedding behaviour.
type BatchEmbedConfig struct {
	// BatchSize is the number of skills to embed per provider call.
	// A value <= 0 defaults to 100.
	BatchSize int

	// RequestsPerSecond caps the rate of embedding-provider API calls.
	// A value <= 0 defaults to 10 (conservative).
	RequestsPerSecond float64

	// OnProgress is an optional callback invoked after each batch.
	OnProgress EmbeddingProgress
}

// EmbeddingProvider is an alias for the Embedder interface in this package,
// used to clarify the batch-embedding contract.
type EmbeddingProvider = Embedder

// ---------------------------------------------------------------------------
// BatchEmbedSkills
// ---------------------------------------------------------------------------

// BatchEmbedSkills generates and stores embeddings for the given skill IDs.
// Skills are processed in configurable batches with rate limiting to avoid
// overwhelming the embedding provider.
//
// Partial failures are tolerated: individual skill embedding errors are
// logged and the function continues with remaining skills. The returned
// error is non-nil only if the entire operation is aborted (e.g. context
// cancellation).
//
// The provider is called with each skill's content (name + description +
// content concatenated) to generate the embedding vector.
func BatchEmbedSkills(
	ctx context.Context,
	pool *Pool,
	skillIDs []uuid.UUID,
	provider EmbeddingProvider,
	cfg BatchEmbedConfig,
) error {
	log := zap.L().With(zap.String("component", "batch_embedding"))

	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = 10
	}

	limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1)
	total := len(skillIDs)
	var succeeded, failed int

	log.Info("starting batch embedding",
		zap.Int("total_skills", total),
		zap.Int("batch_size", cfg.BatchSize),
		zap.Float64("requests_per_second", cfg.RequestsPerSecond))

	for i := 0; i < total; i += cfg.BatchSize {
		select {
		case <-ctx.Done():
			log.Warn("batch embedding cancelled",
				zap.Int("succeeded", succeeded),
				zap.Int("failed", failed),
				zap.Int("remaining", total-succeeded-failed))
			return fmt.Errorf("batch embedding cancelled: %w", ctx.Err())
		default:
		}

		end := i + cfg.BatchSize
		if end > total {
			end = total
		}
		batchIDs := skillIDs[i:end]

		batchSucceeded, batchFailed, err := processBatch(ctx, pool, provider, limiter, batchIDs, log)
		succeeded += batchSucceeded
		failed += batchFailed

		if err != nil {
			log.Error("batch processing error",
				zap.Int("batch_start", i),
				zap.Int("batch_end", end),
				zap.Error(err))
			// Continue with next batch — partial failure tolerance.
		}

		if cfg.OnProgress != nil {
			cfg.OnProgress(succeeded, failed, total)
		}
	}

	log.Info("batch embedding completed",
		zap.Int("succeeded", succeeded),
		zap.Int("failed", failed),
		zap.Int("total", total))

	if failed > 0 {
		return fmt.Errorf("batch embedding completed with %d/%d failures", failed, total)
	}
	return nil
}

// processBatch handles a single batch of skills: fetches content, calls
// the embedding provider, and stores the results.
func processBatch(
	ctx context.Context,
	pool *Pool,
	provider EmbeddingProvider,
	limiter *rate.Limiter,
	skillIDs []uuid.UUID,
	log *zap.Logger,
) (succeeded, failed int, batchErr error) {
	// Fetch skill content for this batch.
	type skillContent struct {
		ID   uuid.UUID
		Name string
		Text string
	}

	var contents []skillContent
	for _, id := range skillIDs {
		var name, title, description, content string
		err := pool.QueryRow(ctx,
			`SELECT name, COALESCE(title,''), COALESCE(description,''), COALESCE(content,'')
			 FROM skills WHERE id = $1`, id,
		).Scan(&name, &title, &description, &content)
		if err != nil {
			log.Warn("failed to fetch skill content for embedding",
				zap.String("skill_id", id.String()),
				zap.Error(err))
			failed++
			continue
		}

		// Build the embedding text: name + title + description + content.
		text := name
		if title != "" {
			text += " " + title
		}
		if description != "" {
			text += " " + description
		}
		if content != "" {
			text += " " + content
		}

		contents = append(contents, skillContent{ID: id, Name: name, Text: text})
	}

	if len(contents) == 0 {
		return 0, failed, nil
	}

	// Rate limit: wait for permission before calling the provider.
	if err := limiter.Wait(ctx); err != nil {
		return 0, failed, fmt.Errorf("rate limiter wait: %w", err)
	}

	// Collect texts for batch embedding.
	texts := make([]string, len(contents))
	for i, c := range contents {
		texts[i] = c.Text
	}

	// Call the embedding provider.
	vectors, err := provider.Embed(ctx, texts)
	if err != nil {
		// Mark all in this sub-batch as failed.
		return 0, len(contents), fmt.Errorf("provider embed: %w", err)
	}

	if len(vectors) != len(contents) {
		return 0, len(contents), fmt.Errorf("provider returned %d vectors for %d inputs",
			len(vectors), len(contents))
	}

	// Store embeddings.
	for i, c := range contents {
		vec := vectors[i]
		if vec == nil || len(vec) != provider.Dimensions() {
			log.Warn("invalid embedding vector, skipping",
				zap.String("skill_id", c.ID.String()),
				zap.String("skill_name", c.Name))
			failed++
			continue
		}

		// Use pgvector format: store as a vector literal.
		vectorStr := formatVector(vec)
		_, err := pool.Exec(ctx,
			`UPDATE skills SET embedding = $1::vector WHERE id = $2`,
			vectorStr, c.ID,
		)
		if err != nil {
			log.Warn("failed to store embedding",
				zap.String("skill_id", c.ID.String()),
				zap.String("skill_name", c.Name),
				zap.Error(err))
			failed++
			continue
		}
		succeeded++
	}

	return succeeded, failed, nil
}

// formatVector converts a float32 slice to a pgvector literal string
// (e.g. "[0.1,0.2,0.3]").
func formatVector(vec []float32) string {
	if len(vec) == 0 {
		return "[]"
	}
	buf := make([]byte, 0, len(vec)*8)
	buf = append(buf, '[')
	for i, v := range vec {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = fmt.Appendf(buf, "%g", v)
	}
	buf = append(buf, ']')
	return string(buf)
}

// ---------------------------------------------------------------------------
// BatchEmbedAllSkills is a convenience function that embeds ALL skills
// in the database that don't yet have an embedding.
// ---------------------------------------------------------------------------

// BatchEmbedAllSkills finds all skills without embeddings and generates
// them using the provided provider. This is useful for initial population
// or after a provider/model change.
func BatchEmbedAllSkills(
	ctx context.Context,
	pool *Pool,
	provider EmbeddingProvider,
	cfg BatchEmbedConfig,
) error {
	log := zap.L().With(zap.String("component", "batch_embedding"))

	rows, err := pool.Query(ctx,
		`SELECT id FROM skills WHERE embedding IS NULL ORDER BY created_at`,
	)
	if err != nil {
		return fmt.Errorf("query unembedded skills: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan skill id: %w", err)
		}
		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate unembedded skills: %w", err)
	}

	if len(ids) == 0 {
		log.Info("no skills require embedding")
		return nil
	}

	log.Info("found skills without embeddings", zap.Int("count", len(ids)))

	// Record start time for rate reporting.
	start := time.Now()
	err = BatchEmbedSkills(ctx, pool, ids, provider, cfg)
	elapsed := time.Since(start)

	if elapsed > 0 && len(ids) > 0 {
		rate := float64(len(ids)) / elapsed.Seconds()
		log.Info("embedding rate",
			zap.Float64("skills_per_second", rate),
			zap.Duration("elapsed", elapsed))
	}

	return err
}
