// cmd/worker is the background worker process for the HelixKnowledge
// Skill Graph System. It runs background jobs including:
//   - Auto-expansion pipeline (skill gap detection and LLM-assisted drafting)
//   - Validation pipeline (multi-layer skill validation with LLM jury)
//   - Code analysis (tree-sitter based pattern extraction)
//   - Registry review (periodic health checks)
//
// Usage:
//
//	go run ./cmd/worker
//	# Or with custom config:
//	go run ./cmd/worker --config=/path/to/config.toml
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/HelixDevelopment/skills/internal/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to TOML configuration file")
	flag.Parse()

	// -----------------------------------------------------------------------
	// 1. Load configuration
	// -----------------------------------------------------------------------
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// -----------------------------------------------------------------------
	// 2. Initialize logger
	// -----------------------------------------------------------------------
	logger, err := initLogger(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("HelixKnowledge worker starting",
		zap.String("version", "1.0.0"),
		zap.String("config_path", configPath),
	)

	// -----------------------------------------------------------------------
	// 3. Connect to database
	// -----------------------------------------------------------------------
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := db.New(cfg.Database)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	cancel()

	// Verify database connectivity
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	if err := pool.Health(ctx); err != nil {
		logger.Fatal("Database health check failed", zap.Error(err))
	}
	cancel()

	logger.Info("Database connected",
		zap.String("host", cfg.Database.Host),
		zap.Int("port", cfg.Database.Port),
	)

	// -----------------------------------------------------------------------
	// 4. Initialize skill store
	// -----------------------------------------------------------------------
	store := skill.NewStore(pool)

	// -----------------------------------------------------------------------
	// 5. Create worker runner
	// -----------------------------------------------------------------------
	runner := worker.NewRunner(pool, store, *cfg, logger)

	// -----------------------------------------------------------------------
	// 6. Start background workers
	// -----------------------------------------------------------------------
	runner.Start()

	logger.Info("Background workers started",
		zap.Bool("auto_expand", cfg.AutoExpand.Enabled),
		zap.Bool("validation", cfg.Validation.Enabled),
		zap.Bool("code_analysis", cfg.CodeAnalysis.Enabled),
	)

	// -----------------------------------------------------------------------
	// 7. Wait for shutdown signal
	// -----------------------------------------------------------------------
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	logger.Info("Worker running. Press Ctrl+C to shutdown.")

	sig := <-sigChan
	logger.Info("Shutdown signal received", zap.String("signal", sig.String()))

	// -----------------------------------------------------------------------
	// 8. Graceful shutdown
	// -----------------------------------------------------------------------
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	runner.Stop(shutdownCtx)

	// Close database pool
	pool.Close()

	logger.Info("Worker shutdown complete")

	// Print final metrics
	metrics := runner.GetMetrics()
	logger.Info("Final metrics",
		zap.Int64("jobs_processed", metrics.JobsProcessed),
		zap.Int64("jobs_failed", metrics.JobsFailed),
		zap.Int64("jobs_retried", metrics.JobsRetried),
		zap.Duration("avg_duration", metrics.AvgDuration),
	)

	// Also print code analysis availability
	parser, _ := codeanalysis.NewTreeSitterParser()
	logger.Info("Code analysis languages available",
		zap.Strings("languages", parser.GetSupportedLanguages()),
	)
}

// ---------------------------------------------------------------------------
// Logger initialization
// ---------------------------------------------------------------------------

// initLogger creates a Zap logger from configuration.
func initLogger(cfg config.LoggingConfig) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	switch cfg.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if cfg.Format == "console" {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	}

	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(os.Stdout),
		level,
	)

	logger := zap.New(core,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)

	return logger, nil
}
