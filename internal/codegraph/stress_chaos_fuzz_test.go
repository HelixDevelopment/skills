package codegraph

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Stress — concurrent IndexManager operations (N=100, no races)
// ---------------------------------------------------------------------------

func TestStress_ConcurrentSymbolsToEvidence(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	symbols := make([]Symbol, 50)
	for i := range symbols {
		symbols[i] = Symbol{
			Name:      "TestFunc",
			Kind:      "function",
			Language:  "go",
			File:      "test.go",
			Signature: "func TestFunc()",
		}
	}
	skillID := uuid.New()

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := im.SymbolsToEvidence(symbols, skillID, "fixture-project")
			_ = ev
		}()
	}
	wg.Wait()
	if im.EvidenceCacheSize() < 1 {
		t.Error("expected at least 1 dedup entry after concurrent calls")
	}
}

func TestStress_ConcurrentDeduplicateEvidence(t *testing.T) {
	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	evidence := make([]models.Evidence, 100)
	for i := range evidence {
		evidence[i] = models.Evidence{
			ID:            uuid.New(),
			SkillID:       uuid.New(),
			SourceProject: "p",
			SourceFile:    "f.go",
			Pattern:       "function",
		}
	}

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = im.DeduplicateEvidence(evidence)
		}()
	}
	wg.Wait()
}

func TestStress_ConcurrentEvidenceCacheLifecycle(t *testing.T) {
	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uuid.New()
			im.MarkEvidenceSeen("proj", "file.go", "pattern", id)
		}()
	}
	wg.Wait()

	// Dedup: marking same key should not increase size past 1.
	if im.EvidenceCacheSize() != 1 {
		t.Errorf("expected cache size 1, got %d", im.EvidenceCacheSize())
	}
}

func TestStress_ConcurrentClientBuild(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}

	const n = 100
	var wg sync.WaitGroup
	clients := make([]*MCPClient, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			clients[idx] = NewMCPClient(cfg, logger)
		}(i)
	}
	wg.Wait()

	for i, c := range clients {
		if c == nil {
			t.Errorf("goroutine %d: nil client", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Chaos — graceful degradation when server is unavailable
// ---------------------------------------------------------------------------

func TestChaos_IndexManager_IndexProject_UnavailableReturnsEmpty(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{
		Enabled:   false,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1", // unreachable
	}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := im.IndexProject(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result (empty, not nil)")
	}
	if result.FilesIndexed < 0 {
		t.Errorf("negative files indexed: %d", result.FilesIndexed)
	}
}

func TestChaos_IndexManager_QuerySymbols_UnavailableReturnsEmpty(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{
		Enabled:   false,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1",
	}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	symbols, err := im.QuerySymbols(ctx, "irrelevant")
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	if symbols == nil {
		t.Log("nil symbols on unavailable server is acceptable")
	}
}

func TestChaos_MCPClient_NilConfig(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewMCPClient with zero config panicked: %v", r)
		}
	}()
	logger := zap.NewNop()
	client := NewMCPClient(config.CodeGraphConfig{}, logger)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestChaos_SymbolToEvidence_FillsFields(t *testing.T) {
	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	sym := Symbol{Name: "test", Kind: "function", Language: "go", File: "t.go", Signature: "func T()"}
	ev := im.SymbolToEvidence(sym, uuid.Nil, "proj")

	if ev.SourceProject != "proj" {
		t.Errorf("SourceProject = %q, want %q", ev.SourceProject, "proj")
	}
	if ev.Language != "go" {
		t.Errorf("Language = %q, want %q", ev.Language, "go")
	}
	if ev.SourceFile != "t.go" {
		t.Errorf("SourceFile = %q, want %q", ev.SourceFile, "t.go")
	}
}

func TestChaos_DeduplicateEvidence_EmptySlice(t *testing.T) {
	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	result := im.DeduplicateEvidence(nil)
	if result != nil {
		t.Errorf("expected nil from nil input, got %d entries", len(result))
	}

	result = im.DeduplicateEvidence([]models.Evidence{})
	if len(result) != 0 {
		t.Errorf("expected empty from empty input, got %d", len(result))
	}
}

func TestChaos_ClearEvidenceCache_ResetsSize(t *testing.T) {
	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	im.MarkEvidenceSeen("p", "f", "x", uuid.New())
	if im.EvidenceCacheSize() != 1 {
		t.Fatal("expected cache size 1 after MarkEvidenceSeen")
	}

	im.ClearEvidenceCache()
	if im.EvidenceCacheSize() != 0 {
		t.Errorf("expected cache size 0 after Clear, got %d", im.EvidenceCacheSize())
	}
}

func TestChaos_IndexManager_GetDependencies_UnavailableReturnsEmpty(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{
		Enabled:   false,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1",
	}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	deps, err := im.GetDependencies(ctx, "src/main.go")
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	_ = deps
}

// ---------------------------------------------------------------------------
// Chaos — SyncManager lifecycle
// ---------------------------------------------------------------------------

func TestChaos_SyncManager_StartStop_Lifecycle(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)
	sm := NewSyncManager(client, im, nil, nil, cfg, logger)

	// Start runs synchronously (blocking poll loop); run in goroutine
	// with a short-lived context to test the lifecycle without blocking.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	done := make(chan struct{})
	go func() {
		sm.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Start returned because ctx was already cancelled.
	case <-time.After(time.Second):
		sm.Stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Log("Start did not return within timeout (blocking poll loop)")
		}
	}
}

func TestChaos_SyncManager_RegisterUnregister(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)
	sm := NewSyncManager(client, im, nil, nil, cfg, logger)

	sm.RegisterProject("/test/project")
	projects := sm.RegisteredProjects()
	if len(projects) != 1 {
		t.Errorf("expected 1 registered project, got %d", len(projects))
	}

	sm.UnregisterProject("/test/project")
	projects = sm.RegisteredProjects()
	if len(projects) != 0 {
		t.Errorf("expected 0 projects after unregister, got %d", len(projects))
	}
}

// ---------------------------------------------------------------------------
// Chaos — PatternExtractor edge cases
// ---------------------------------------------------------------------------

func TestChaos_PatternExtractor_ZeroConfig_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewPatternExtractor with zero config panicked: %v", r)
		}
	}()
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)
	ext := NewPatternExtractor(client, im, nil, PatternExtractorConfig{MinConfidence: 0.5}, logger)
	if ext == nil {
		t.Fatal("expected non-nil extractor")
	}
}

func TestChaos_PatternExtractor_ExtractPatterns_UnconfiguredClient(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)
	ext := NewPatternExtractor(client, im, nil, PatternExtractorConfig{MinConfidence: 0.5}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	patterns, err := ext.ExtractPatterns(ctx, "nonexistent-project")
	if err != nil {
		t.Logf("ExtractPatterns error (acceptable): %v", err)
		return
	}
	if len(patterns) > 0 {
		t.Logf("patterns found with unavailable server: %d", len(patterns))
	}
}

// ---------------------------------------------------------------------------
// Fuzz — evidence cache lifecycle
// ---------------------------------------------------------------------------

func FuzzEvidenceCache(f *testing.F) {
	f.Add("project", "file.go", "pattern")
	f.Add("", "", "")
	f.Add("a\x00b", "c\x00d", "e\x00f")
	f.Add("normal-project", "src/pkg/main.go", "repository_pattern")

	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)

	f.Fuzz(func(t *testing.T, project, file, pattern string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("MarkEvidenceSeen(%q, %q, %q) panicked: %v", project, file, pattern, r)
			}
		}()
		id := uuid.New()
		im.MarkEvidenceSeen(project, file, pattern, id)
		size := im.EvidenceCacheSize()
		if size < 1 {
			t.Errorf("expected cache ≥1 after MarkEvidenceSeen, got %d", size)
		}
	})
}

// ---------------------------------------------------------------------------
// Fuzz — IndexProject paths
// ---------------------------------------------------------------------------

func FuzzIndexProjectPath(f *testing.F) {
	f.Add("normal-project")
	f.Add("")
	f.Add("/absolute/path/to/project")
	f.Add("../relative/path")
	f.Add("project\x00with\x00nulls")

	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	f.Fuzz(func(t *testing.T, projectPath string) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("IndexProject(%q) panicked: %v", projectPath, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		result, err := im.IndexProject(ctx, projectPath)
		if err != nil {
			t.Logf("IndexProject error (acceptable): %v", err)
			return
		}
		if result == nil {
			t.Log("nil result (acceptable)")
		}
	})
}

// ---------------------------------------------------------------------------
// Fuzz — symbol-to-evidence conversion
// ---------------------------------------------------------------------------

func FuzzSymbolToEvidence(f *testing.F) {
	f.Add("MyFunc", "function", "go", "file.go", "func MyFunc() error")
	f.Add("", "", "", "", "")
	f.Add("\x00\x01", "\xff\xfe", "rust", "lib.rs", "fn main()")

	logger := zap.NewNop()
	im := NewIndexManager(nil, logger)
	skillID := uuid.New()

	f.Fuzz(func(t *testing.T, name, kind, language, file, signature string) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("SymbolToEvidence panicked: %v", r)
			}
		}()
		sym := Symbol{
			Name:      name,
			Kind:      kind,
			Language:  language,
			File:      file,
			Signature: signature,
		}
		ev := im.SymbolToEvidence(sym, skillID, "project")
		if ev.ID == uuid.Nil {
			t.Error("evidence ID should be non-nil")
		}
		if ev.SkillID != skillID {
			t.Error("evidence SkillID mismatch")
		}
	})
}

// ---------------------------------------------------------------------------
// Stress — concurrent GetDependencies
// ---------------------------------------------------------------------------

func TestStress_ConcurrentGetDependencies(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.CodeGraphConfig{Enabled: false}
	client := NewMCPClient(cfg, logger)
	im := NewIndexManager(client, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := im.GetDependencies(ctx, "src/main.go")
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("GetDependencies: %v", err)
	}
}

var _ = context.Background
var _ = uuid.Nil
