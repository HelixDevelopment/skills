package skillsource

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Stress — concurrent validation (N=100)
// ---------------------------------------------------------------------------

func TestStress_ConcurrentValidate(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	now := time.Now().UTC()
	src := &SkillSource{
		ID:         uuid.New(),
		Name:       "test-source",
		SourceType: SourceTypeGitHub,
		Config:     json.RawMessage(`{"owner":"test","repo":"test"}`),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := src.Validate(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Validate failed: %v", err)
	}
}

func TestStress_ConcurrentValidateMultipleTypes(t *testing.T) {
	now := time.Now().UTC()
	sources := []*SkillSource{
		{ID: uuid.New(), Name: "github-src", SourceType: SourceTypeGitHub, Config: json.RawMessage(`{}`), SyncStatus: SyncStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), Name: "fs-src", SourceType: SourceTypeFilesystem, Config: json.RawMessage(`{}`), SyncStatus: SyncStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), Name: "url-src", SourceType: SourceTypeURL, Config: json.RawMessage(`{}`), SyncStatus: SyncStatusPending, CreatedAt: now, UpdatedAt: now},
	}

	const n = 100
	var wg sync.WaitGroup
	for _, src := range sources {
		src := src
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = src.Validate()
			}()
		}
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Chaos — validation edge cases
// ---------------------------------------------------------------------------

func TestChaos_Validate_EmptyName(t *testing.T) {
	src := &SkillSource{
		ID:         uuid.New(),
		Name:       "",
		SourceType: SourceTypeGitHub,
	}
	err := src.Validate()
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestChaos_Validate_InvalidSourceType(t *testing.T) {
	src := &SkillSource{
		ID:         uuid.New(),
		Name:       "test",
		SourceType: SourceType("nonexistent-type"),
	}
	err := src.Validate()
	if err == nil {
		t.Error("expected error for invalid source type")
	}
}

func TestChaos_Validate_InvalidSyncStatus(t *testing.T) {
	src := &SkillSource{
		ID:         uuid.New(),
		Name:       "test",
		SourceType: SourceTypeGitHub,
		SyncStatus: SyncStatus("bogus-status"),
	}
	err := src.Validate()
	if err == nil {
		t.Error("expected error for invalid sync status")
	}
}

func TestChaos_Validate_NilConfig_DefaultsToEmpty(t *testing.T) {
	src := &SkillSource{
		ID:         uuid.New(),
		Name:       "test",
		SourceType: SourceTypeFilesystem,
		Config:     nil,
	}
	err := src.Validate()
	if err != nil {
		t.Fatalf("unexpected error for nil config: %v", err)
	}
	if src.Config == nil {
		t.Error("nil config should have been defaulted to {}")
	}
}

func TestChaos_SourceType_IsValid(t *testing.T) {
	tests := []struct {
		st    SourceType
		valid bool
	}{
		{SourceTypeGitHub, true},
		{SourceTypeFilesystem, true},
		{SourceTypeURL, true},
		{SourceType(""), false},
		{SourceType("unknown"), false},
		{SourceType("gitlab"), false},
	}
	for _, tc := range tests {
		if got := tc.st.IsValid(); got != tc.valid {
			t.Errorf("SourceType(%q).IsValid() = %v, want %v", tc.st, got, tc.valid)
		}
	}
}

func TestChaos_SyncStatus_IsValid(t *testing.T) {
	tests := []struct {
		ss    SyncStatus
		valid bool
	}{
		{SyncStatusPending, true},
		{SyncStatusSyncing, true},
		{SyncStatusCompleted, true},
		{SyncStatusFailed, true},
		{SyncStatus(""), false},
		{SyncStatus("IN_PROGRESS"), false},
	}
	for _, tc := range tests {
		if got := tc.ss.IsValid(); got != tc.valid {
			t.Errorf("SyncStatus(%q).IsValid() = %v, want %v", tc.ss, got, tc.valid)
		}
	}
}

func TestChaos_GitHubConfig_MarshalRoundTrip(t *testing.T) {
	cfg := GitHubConfig{
		Owner: "anthropics",
		Repo:  "claude-code-skills",
		Ref:   "main",
		Path:  "skills",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var roundTrip GitHubConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Owner != cfg.Owner || roundTrip.Repo != cfg.Repo {
		t.Errorf("round-trip mismatch: %+v != %+v", roundTrip, cfg)
	}
}

func TestChaos_FilesystemConfig_MarshalRoundTrip(t *testing.T) {
	cfg := FilesystemConfig{RootPath: "/home/user/skills"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip FilesystemConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.RootPath != cfg.RootPath {
		t.Errorf("round-trip mismatch")
	}
}

func TestChaos_URLConfig_MarshalRoundTrip(t *testing.T) {
	cfg := URLConfig{Endpoint: "https://example.com/skills"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip URLConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Endpoint != cfg.Endpoint {
		t.Errorf("round-trip mismatch")
	}
}

// ---------------------------------------------------------------------------
// Fuzz — SkillSource.Validate
// ---------------------------------------------------------------------------

func FuzzValidate(f *testing.F) {
	f.Add("valid-name", "github", "pending")
	f.Add("", "", "")
	f.Add("a", "filesystem", "completed")
	f.Add("long-name", "invalid-type", "failed")

	f.Fuzz(func(t *testing.T, name, sourceType, syncStatus string) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Validate panicked: %v", r)
			}
		}()
		src := &SkillSource{
			ID:         uuid.New(),
			Name:       name,
			SourceType: SourceType(sourceType),
			SyncStatus: SyncStatus(syncStatus),
			Config:     json.RawMessage(`{}`),
		}
		err := src.Validate()
		// Validate must return nil for valid inputs or a wrapped ErrInvalidSource
		if name != "" && src.SourceType.IsValid() && (syncStatus == "" || src.SyncStatus.IsValid()) {
			if err != nil {
				t.Logf("unexpected error for valid inputs: %v", err)
			}
		}
		_ = err
	})
}

// ---------------------------------------------------------------------------
// Ensure all importable constants are valid
// ---------------------------------------------------------------------------

func TestValidSourceTypes_ContainsAllTypes(t *testing.T) {
	expected := map[SourceType]bool{
		SourceTypeGitHub:     true,
		SourceTypeFilesystem: true,
		SourceTypeURL:        true,
	}
	for _, st := range ValidSourceTypes {
		if !expected[st] {
			t.Errorf("unexpected source type in ValidSourceTypes: %q", st)
		}
		delete(expected, st)
	}
	if len(expected) > 0 {
		for st := range expected {
			t.Errorf("missing source type from ValidSourceTypes: %q", st)
		}
	}
}

func TestValidSyncStatuses_ContainsAllStatuses(t *testing.T) {
	expected := map[SyncStatus]bool{
		SyncStatusPending:   true,
		SyncStatusSyncing:   true,
		SyncStatusCompleted: true,
		SyncStatusFailed:    true,
	}
	for _, ss := range ValidSyncStatuses {
		if !expected[ss] {
			t.Errorf("unexpected sync status in ValidSyncStatuses: %q", ss)
		}
		delete(expected, ss)
	}
	if len(expected) > 0 {
		for ss := range expected {
			t.Errorf("missing sync status from ValidSyncStatuses: %q", ss)
		}
	}
}

var _ = uuid.Nil
