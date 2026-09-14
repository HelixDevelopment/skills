package skillsource

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// SourceType / SyncStatus validation
// ---------------------------------------------------------------------------

func TestSourceType_IsValid(t *testing.T) {
	tests := []struct {
		st   SourceType
		want bool
	}{
		{SourceTypeGitHub, true},
		{SourceTypeFilesystem, true},
		{SourceTypeURL, true},
		{"", false},
		{"gitlab", false},
		{"GITHUB", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := tt.st.IsValid(); got != tt.want {
			t.Errorf("SourceType(%q).IsValid() = %v, want %v", tt.st, got, tt.want)
		}
	}
}

func TestSyncStatus_IsValid(t *testing.T) {
	tests := []struct {
		ss   SyncStatus
		want bool
	}{
		{SyncStatusPending, true},
		{SyncStatusSyncing, true},
		{SyncStatusCompleted, true},
		{SyncStatusFailed, true},
		{"", false},
		{"running", false},
		{"PENDING", false}, // case-sensitive
	}
	for _, tt := range tests {
		if got := tt.ss.IsValid(); got != tt.want {
			t.Errorf("SyncStatus(%q).IsValid() = %v, want %v", tt.ss, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SkillSource.Validate
// ---------------------------------------------------------------------------

func TestSkillSource_Validate(t *testing.T) {
	validSource := func() *SkillSource {
		return &SkillSource{
			ID:         uuid.New(),
			Name:       "test-source",
			SourceType: SourceTypeGitHub,
			Config:     json.RawMessage(`{"owner":"anthropics","repo":"skills"}`),
			Enabled:    true,
			SyncStatus: SyncStatusPending,
		}
	}

	t.Run("valid source passes", func(t *testing.T) {
		s := validSource()
		if err := s.Validate(); err != nil {
			t.Errorf("Validate() unexpected error: %v", err)
		}
	})

	t.Run("empty name fails", func(t *testing.T) {
		s := validSource()
		s.Name = ""
		err := s.Validate()
		if err == nil {
			t.Fatal("Validate() expected error for empty name")
		}
		if !isErrInvalidSource(err) {
			t.Errorf("Validate() error = %v, want wrapped ErrInvalidSource", err)
		}
	})

	t.Run("invalid source type fails", func(t *testing.T) {
		s := validSource()
		s.SourceType = SourceType("gitlab")
		err := s.Validate()
		if err == nil {
			t.Fatal("Validate() expected error for invalid source type")
		}
		if !isErrInvalidSource(err) {
			t.Errorf("Validate() error = %v, want wrapped ErrInvalidSource", err)
		}
	})

	t.Run("nil config defaults to empty object", func(t *testing.T) {
		s := validSource()
		s.Config = nil
		if err := s.Validate(); err != nil {
			t.Errorf("Validate() unexpected error: %v", err)
		}
		if string(s.Config) != "{}" {
			t.Errorf("Validate() did not default nil config; got %s", string(s.Config))
		}
	})

	t.Run("invalid sync status fails", func(t *testing.T) {
		s := validSource()
		s.SyncStatus = SyncStatus("running")
		err := s.Validate()
		if err == nil {
			t.Fatal("Validate() expected error for invalid sync status")
		}
		if !isErrInvalidSource(err) {
			t.Errorf("Validate() error = %v, want wrapped ErrInvalidSource", err)
		}
	})

	t.Run("empty sync status passes (defaults on create)", func(t *testing.T) {
		s := validSource()
		s.SyncStatus = ""
		if err := s.Validate(); err != nil {
			t.Errorf("Validate() unexpected error for empty sync status: %v", err)
		}
	})
}

// isErrInvalidSource reports whether err wraps ErrInvalidSource.
func isErrInvalidSource(err error) bool {
	return err != nil && contains(err.Error(), ErrInvalidSource.Error())
}
