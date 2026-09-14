package skillsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/source/mapper"
	"github.com/HelixDevelopment/skills/internal/source/skillmd"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Mock stores for unit testing the orchestrator
// ---------------------------------------------------------------------------

// mockSourceStore implements SourceStoreReader for orchestrator tests.
type mockSourceStore struct {
	sources map[uuid.UUID]*SkillSource
	getErr  error // injected error for GetByID
	syncErr error // injected error for UpdateSyncStatus
}

func newMockSourceStore() *mockSourceStore {
	return &mockSourceStore{sources: make(map[uuid.UUID]*SkillSource)}
}

func (m *mockSourceStore) GetByID(_ context.Context, id uuid.UUID) (*SkillSource, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.sources[id]
	if !ok {
		return nil, ErrSourceNotFound
	}
	return s, nil
}

func (m *mockSourceStore) UpdateSyncStatus(_ context.Context, id uuid.UUID, status SyncStatus, errMsg string) error {
	if m.syncErr != nil {
		return m.syncErr
	}
	s, ok := m.sources[id]
	if !ok {
		return ErrSourceNotFound
	}
	s.SyncStatus = status
	s.ErrorMessage = errMsg
	return nil
}

// mockSkillStore implements SkillStoreWriter for orchestrator tests.
type mockSkillStore struct {
	skills    map[string]*models.Skill
	createErr error // injected error for Create
	getErr    error // injected error for GetByName
}

func newMockSkillStore() *mockSkillStore {
	return &mockSkillStore{skills: make(map[string]*models.Skill)}
}

func (m *mockSkillStore) Create(_ context.Context, skill *models.Skill) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.skills[skill.Name] = skill
	return nil
}

func (m *mockSkillStore) GetByName(_ context.Context, name string) (*models.Skill, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.skills[name]
	if !ok {
		return nil, skillmd.ErrMissingName // stand-in for skill.ErrSkillNotFound
	}
	return s, nil
}

// mockFetcher implements Fetcher for orchestrator tests. Not used directly
// in SyncSource tests (which test via fetchSource dispatch), but available
// for unit testing individual pipeline stages.
type mockFetcher struct {
	items []FetchResult
	err   error
}

func (m *mockFetcher) Fetch(_ context.Context) ([]FetchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.items, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newGitHubSource creates a test SkillSource of type GitHub with the given
// config.
func newGitHubSource(id uuid.UUID, name string, cfg GitHubConfig) *SkillSource {
	raw, _ := json.Marshal(cfg)
	return &SkillSource{
		ID:         id,
		Name:       name,
		SourceType: SourceTypeGitHub,
		Config:     json.RawMessage(raw),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
	}
}

// newFilesystemSource creates a test SkillSource of type filesystem.
func newFilesystemSource(id uuid.UUID, name string, cfg FilesystemConfig) *SkillSource {
	raw, _ := json.Marshal(cfg)
	return &SkillSource{
		ID:         id,
		Name:       name,
		SourceType: SourceTypeFilesystem,
		Config:     json.RawMessage(raw),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
	}
}

// sampleSKILLMD returns a minimal valid SKILL.md byte slice.
func sampleSKILLMD(name, description string) []byte {
	return []byte(`---
name: ` + name + `
description: ` + description + `
license: MIT
---
# ` + name + `

This is the body of ` + name + `.
`)
}

// ---------------------------------------------------------------------------
// Orchestrator tests
// ---------------------------------------------------------------------------

func TestOrchestrator_SyncSource_SourceNotFound(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	_, err := o.SyncSource(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("SyncSource() expected error for non-existent source")
	}
	if !isErrSourceNotFound(err) {
		t.Errorf("SyncSource() error = %v, want ErrSourceNotFound", err)
	}
}

func TestOrchestrator_SyncSource_DisabledSource(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	id := uuid.New()
	src := newGitHubSource(id, "disabled-source", GitHubConfig{Owner: "test", Repo: "repo"})
	src.Enabled = false
	srcStore.sources[id] = src

	result, err := o.SyncSource(context.Background(), id)
	if err != nil {
		t.Fatalf("SyncSource() error: %v", err)
	}
	if result.Fetched != 0 {
		t.Errorf("SyncSource() Fetched = %d, want 0 (disabled)", result.Fetched)
	}
	if result.Parsed != 0 {
		t.Errorf("SyncSource() Parsed = %d, want 0 (disabled)", result.Parsed)
	}
}

func TestOrchestrator_SyncSource_GetByIDError(t *testing.T) {
	srcStore := newMockSourceStore()
	srcStore.getErr = errors.New("database connection lost")
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	_, err := o.SyncSource(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("SyncSource() expected error when GetByID fails")
	}
}

func TestOrchestrator_SyncSource_UpdateSyncStatusError(t *testing.T) {
	srcStore := newMockSourceStore()
	srcStore.syncErr = errors.New("cannot write to database")
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	id := uuid.New()
	src := newGitHubSource(id, "test", GitHubConfig{Owner: "o", Repo: "r"})
	srcStore.sources[id] = src

	_, err := o.SyncSource(context.Background(), id)
	if err == nil {
		t.Fatal("SyncSource() expected error when UpdateSyncStatus(syncing) fails")
	}
}

func TestOrchestrator_SyncSource_UnsupportedSourceType(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	id := uuid.New()
	src := &SkillSource{
		ID:         id,
		Name:       "bad-type",
		SourceType: SourceType("gitlab"),
		Config:     json.RawMessage(`{}`),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
	}
	srcStore.sources[id] = src

	result, err := o.SyncSource(context.Background(), id)
	// The fetch should fail with an unsupported source type error.
	if err == nil {
		t.Fatal("SyncSource() expected error for unsupported source type")
	}
	// Verify the source was marked as failed.
	if src.SyncStatus != SyncStatusFailed {
		t.Errorf("SyncStatus = %q, want %q after failure", src.SyncStatus, SyncStatusFailed)
	}
	_ = result
}

func TestOrchestrator_SyncSource_URLSourceTypeNotImplemented(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)

	id := uuid.New()
	src := &SkillSource{
		ID:         id,
		Name:       "url-source",
		SourceType: SourceTypeURL,
		Config:     json.RawMessage(`{"endpoint":"https://example.com/skills"}`),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
	}
	srcStore.sources[id] = src

	_, err := o.SyncSource(context.Background(), id)
	if err == nil {
		t.Fatal("SyncSource() expected error for URL source type (not implemented)")
	}
}

// ---------------------------------------------------------------------------
// Pipeline stage tests
// ---------------------------------------------------------------------------

func TestOrchestrator_parseContent(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)

	t.Run("valid SKILL.md parses successfully", func(t *testing.T) {
		items := []FetchResult{
			{Path: "good/SKILL.md", Content: sampleSKILLMD("test-skill", "A test skill")},
		}
		parsed, errs := o.parseContent(items)
		if len(parsed) != 1 {
			t.Fatalf("parseContent() returned %d parsed, want 1", len(parsed))
		}
		if len(errs) != 0 {
			t.Errorf("parseContent() returned %d errors, want 0", len(errs))
		}
		if parsed[0].Name != "test-skill" {
			t.Errorf("parsed Name = %q, want %q", parsed[0].Name, "test-skill")
		}
	})

	t.Run("invalid content produces error but continues", func(t *testing.T) {
		items := []FetchResult{
			{Path: "bad/no-frontmatter.md", Content: []byte("no frontmatter here")},
			{Path: "good/SKILL.md", Content: sampleSKILLMD("valid", "Valid skill")},
		}
		parsed, errs := o.parseContent(items)
		if len(parsed) != 1 {
			t.Fatalf("parseContent() returned %d parsed, want 1 (the valid one)", len(parsed))
		}
		if len(errs) != 1 {
			t.Errorf("parseContent() returned %d errors, want 1 (the invalid one)", len(errs))
		}
	})

	t.Run("missing name produces error", func(t *testing.T) {
		items := []FetchResult{
			{Path: "noname/SKILL.md", Content: []byte("---\ndescription: no name\n---\nbody\n")},
		}
		parsed, errs := o.parseContent(items)
		if len(parsed) != 0 {
			t.Errorf("parseContent() returned %d parsed, want 0 (no name)", len(parsed))
		}
		if len(errs) != 1 {
			t.Errorf("parseContent() returned %d errors, want 1", len(errs))
		}
	})

	t.Run("empty items returns empty", func(t *testing.T) {
		parsed, errs := o.parseContent(nil)
		if len(parsed) != 0 || len(errs) != 0 {
			t.Errorf("parseContent(nil) returned parsed=%d, errs=%d; want 0, 0", len(parsed), len(errs))
		}
	})
}

func TestOrchestrator_mapSkills(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	o.licenseAllowlist = []string{"MIT", "Apache-2.0"}

	source := &SkillSource{
		Name:       "test-source",
		SourceType: SourceTypeGitHub,
		Config:     json.RawMessage(`{"owner":"anthropics","repo":"skills","ref":"main"}`),
	}

	t.Run("valid parsed skill maps successfully", func(t *testing.T) {
		ps, err := skillmd.Parse(sampleSKILLMD("my-skill", "Description"), "my-skill/SKILL.md")
		if err != nil {
			t.Fatalf("Parse() error: %v", err)
		}
		results, errs := o.mapSkills([]*skillmd.ParsedSkill{ps}, source)
		if len(results) != 1 {
			t.Fatalf("mapSkills() returned %d results, want 1", len(results))
		}
		if len(errs) != 0 {
			t.Errorf("mapSkills() returned %d errors, want 0", len(errs))
		}
		// Name should be namespaced: source.Name + "." + parsed.Name
		expectedName := "test-source.my-skill"
		if results[0].Skill.Name != expectedName {
			t.Errorf("mapped Name = %q, want %q", results[0].Skill.Name, expectedName)
		}
		// MIT is in the allowlist, so LicenseSkipped should be false.
		if results[0].LicenseSkipped {
			t.Error("LicenseSkipped = true, want false (MIT is in allowlist)")
		}
	})

	t.Run("disallowed license produces license-gated stub", func(t *testing.T) {
		raw := []byte(`---
name: gpl-skill
description: A GPL skill
license: GPL-3.0
---
# GPL Skill

Real body.
`)
		ps, err := skillmd.Parse(raw, "gpl-skill/SKILL.md")
		if err != nil {
			t.Fatalf("Parse() error: %v", err)
		}
		results, errs := o.mapSkills([]*skillmd.ParsedSkill{ps}, source)
		if len(results) != 1 {
			t.Fatalf("mapSkills() returned %d results, want 1", len(results))
		}
		if len(errs) != 0 {
			t.Errorf("mapSkills() returned %d errors, want 0", len(errs))
		}
		if !results[0].LicenseSkipped {
			t.Error("LicenseSkipped = false, want true (GPL-3.0 not in allowlist)")
		}
		if results[0].Skill.Content == "Real body." {
			t.Error("Content should be a stub, not the real body")
		}
	})

	t.Run("nil parsed list returns empty", func(t *testing.T) {
		results, errs := o.mapSkills(nil, source)
		if len(results) != 0 || len(errs) != 0 {
			t.Errorf("mapSkills(nil) returned results=%d, errs=%d; want 0, 0", len(results), len(errs))
		}
	})
}

func TestOrchestrator_dedupSkills(t *testing.T) {
	t.Run("new skills are included for import", func(t *testing.T) {
		skillStore := newMockSkillStore()
		o := NewOrchestrator(nil, skillStore, nil)

		mapped := []*mapper.Result{
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.skill-a"}},
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.skill-b"}},
		}
		toImport, skipped := o.dedupSkills(context.Background(), mapped)
		if len(toImport) != 2 {
			t.Errorf("dedupSkills() toImport = %d, want 2", len(toImport))
		}
		if skipped != 0 {
			t.Errorf("dedupSkills() skipped = %d, want 0", skipped)
		}
	})

	t.Run("existing skills are included (upsert path)", func(t *testing.T) {
		skillStore := newMockSkillStore()
		// Pre-populate with an existing skill.
		skillStore.skills["source.existing"] = &models.Skill{ID: uuid.New(), Name: "source.existing"}
		o := NewOrchestrator(nil, skillStore, nil)

		mapped := []*mapper.Result{
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.existing"}},
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.new"}},
		}
		toImport, _ := o.dedupSkills(context.Background(), mapped)
		// Both are included because the current implementation always
		// re-imports (Create's upsert handles idempotency).
		if len(toImport) != 2 {
			t.Errorf("dedupSkills() toImport = %d, want 2 (upsert path)", len(toImport))
		}
	})
}

func TestOrchestrator_importSkills(t *testing.T) {
	t.Run("successful imports", func(t *testing.T) {
		skillStore := newMockSkillStore()
		o := NewOrchestrator(nil, skillStore, nil)

		toImport := []*mapper.Result{
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.skill-1"}},
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.skill-2"}},
		}
		imported, errs := o.importSkills(context.Background(), toImport)
		if imported != 2 {
			t.Errorf("importSkills() imported = %d, want 2", imported)
		}
		if len(errs) != 0 {
			t.Errorf("importSkills() errors = %d, want 0", len(errs))
		}
		if _, ok := skillStore.skills["source.skill-1"]; !ok {
			t.Error("skill-1 not found in store after import")
		}
		if _, ok := skillStore.skills["source.skill-2"]; !ok {
			t.Error("skill-2 not found in store after import")
		}
	})

	t.Run("create error is non-fatal", func(t *testing.T) {
		skillStore := newMockSkillStore()
		skillStore.createErr = errors.New("database write failed")
		o := NewOrchestrator(nil, skillStore, nil)

		toImport := []*mapper.Result{
			{Skill: &models.Skill{ID: uuid.New(), Name: "source.skill-1"}},
		}
		imported, errs := o.importSkills(context.Background(), toImport)
		if imported != 0 {
			t.Errorf("importSkills() imported = %d, want 0 (create failed)", imported)
		}
		if len(errs) != 1 {
			t.Errorf("importSkills() errors = %d, want 1", len(errs))
		}
	})

	t.Run("empty list returns zero", func(t *testing.T) {
		skillStore := newMockSkillStore()
		o := NewOrchestrator(nil, skillStore, nil)

		imported, errs := o.importSkills(context.Background(), nil)
		if imported != 0 || len(errs) != 0 {
			t.Errorf("importSkills(nil) = imported=%d, errs=%d; want 0, 0", imported, len(errs))
		}
	})
}

// ---------------------------------------------------------------------------
// Helper tests
// ---------------------------------------------------------------------------

func TestIsSKILLMD(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"SKILL.md", true},
		{"skills/SKILL.md", true},
		{"deep/nested/path/SKILL.md", true},
		{"skill.md", true},        // case-insensitive
		{"Skill.md", true},        // case-insensitive
		{"skills/skill.md", true}, // case-insensitive
		{"README.md", false},
		{"skill.json", false},
		{"SKILL.md.bak", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSKILLMD(tt.path); got != tt.want {
			t.Errorf("isSKILLMD(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestBuildSourcePermalink(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)

	t.Run("github source with path", func(t *testing.T) {
		src := newGitHubSource(uuid.New(), "test", GitHubConfig{
			Owner: "anthropics", Repo: "skills", Ref: "main", Path: "community",
		})
		got := o.buildSourcePermalink(src)
		want := "https://github.com/anthropics/skills/tree/main/community"
		if got != want {
			t.Errorf("buildSourcePermalink() = %q, want %q", got, want)
		}
	})

	t.Run("github source without path", func(t *testing.T) {
		src := newGitHubSource(uuid.New(), "test", GitHubConfig{
			Owner: "anthropics", Repo: "skills", Ref: "v1.0",
		})
		got := o.buildSourcePermalink(src)
		want := "https://github.com/anthropics/skills/tree/v1.0"
		if got != want {
			t.Errorf("buildSourcePermalink() = %q, want %q", got, want)
		}
	})

	t.Run("github source defaults ref to main", func(t *testing.T) {
		src := newGitHubSource(uuid.New(), "test", GitHubConfig{
			Owner: "anthropics", Repo: "skills",
		})
		got := o.buildSourcePermalink(src)
		want := "https://github.com/anthropics/skills/tree/main"
		if got != want {
			t.Errorf("buildSourcePermalink() = %q, want %q", got, want)
		}
	})

	t.Run("non-github source returns empty", func(t *testing.T) {
		src := newFilesystemSource(uuid.New(), "test", FilesystemConfig{RootPath: "/tmp"})
		got := o.buildSourcePermalink(src)
		if got != "" {
			t.Errorf("buildSourcePermalink() = %q, want empty for filesystem", got)
		}
	})
}

func TestUnmarshalConfig(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		raw := json.RawMessage(`{"owner":"test","repo":"skills"}`)
		var cfg GitHubConfig
		if err := unmarshalConfig(raw, &cfg); err != nil {
			t.Fatalf("unmarshalConfig() error: %v", err)
		}
		if cfg.Owner != "test" || cfg.Repo != "skills" {
			t.Errorf("unmarshalConfig() = %+v", cfg)
		}
	})

	t.Run("empty bytes returns nil", func(t *testing.T) {
		var cfg GitHubConfig
		if err := unmarshalConfig(nil, &cfg); err != nil {
			t.Fatalf("unmarshalConfig(nil) error: %v", err)
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		var cfg GitHubConfig
		err := unmarshalConfig(json.RawMessage(`{invalid`), &cfg)
		if err == nil {
			t.Fatal("unmarshalConfig() expected error for invalid JSON")
		}
	})
}

// ---------------------------------------------------------------------------
// NewOrchestrator tests
// ---------------------------------------------------------------------------

func TestNewOrchestrator_NilLogger(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)
	if o.logger == nil {
		t.Fatal("NewOrchestrator(nil logger) should default to no-op logger")
	}
}

func TestWithLicenseAllowlist(t *testing.T) {
	srcStore := newMockSourceStore()
	skillStore := newMockSkillStore()
	o := NewOrchestrator(srcStore, skillStore, nil)
	allowlist := []string{"MIT", "Apache-2.0"}
	o.WithLicenseAllowlist(allowlist)
	if len(o.licenseAllowlist) != 2 {
		t.Errorf("licenseAllowlist length = %d, want 2", len(o.licenseAllowlist))
	}
}

// Ensure the mapper package is used (the import is needed for mapper.Result
// in test assertions; this blank reference prevents the compiler from
// flagging unused imports if future tests are added).
var _ = (*mapper.Result)(nil)
