package mcp

import (
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/HelixDevelopment/skills/internal/models"
)

// ---------------------------------------------------------------------------
// Regression guard for the LIVE MCP skill_create create-path invariant:
//
//	A skill created via the MCP skill_create tool is persisted as `draft` and
//	can NEVER be promoted to `validated`/`active` at creation, regardless of
//	the validation verdict OR any status value in the submitted TOML.
//
// The only prior test covering "promote only on pass" (see
// internal/api/skills_validation_test.go) exercises the DEAD REST create
// path (handleCreateSkill), not the LIVE MCP path. This file closes that gap
// for the live path.
//
// buildSkillFromTOML is the EXACT function the live skill_create tool handler
// invokes (via validateForCreate, see server.go + registerSkillCreate in
// tools.go) to build the in-memory model used for pre-persistence validation
// — this test exercises the real, live code path, not a re-implementation.
//
// The invariant is structural, not incidental:
//  1. buildSkillFromTOML hardcodes Status: models.SkillStatusDraft.
//  2. models.TOMLSkillDef carries no "status" field at all, so a submitted
//     `status = "..."` key in the TOML document is simply an unrecognized,
//     silently-ignored key under the normal (non-strict) toml.Unmarshal.
//  3. models.Skill.Status itself carries the `toml:"-"` tag, which the TOML
//     decoder treats as "always skip this field" (see BurntSushi/toml
//     type_fields.go: getOptions(...).skip) — so even a hypothetical future
//     refactor that unmarshaled TOML directly into models.Skill could not
//     set Status from a submitted "status" key either.
// ---------------------------------------------------------------------------

// TestBuildSkillFromTOML_AlwaysDraftsRegardlessOfSubmittedStatus proves
// invariant (1)+(2) above against the real buildSkillFromTOML function.
func TestBuildSkillFromTOML_AlwaysDraftsRegardlessOfSubmittedStatus(t *testing.T) {
	tests := []struct {
		name          string
		claimedStatus string
	}{
		{"submitted status=active", "active"},
		{"submitted status=validated", "validated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tomlDoc := `
[skill]
name = "sneaky-skill"
version = "1.0.0"
title = "Sneaky Skill"
description = "attempts to self-promote at creation"
content = "# doc"
status = "` + tt.claimedStatus + `"
`
			m, err := buildSkillFromTOML([]byte(tomlDoc))
			if err != nil {
				t.Fatalf("buildSkillFromTOML: unexpected error: %v", err)
			}
			if m.Status != models.SkillStatusDraft {
				t.Errorf("Status = %q, want %q (a submitted TOML status MUST NEVER promote a skill at creation, regardless of the requested value or any later validation verdict)",
					m.Status, models.SkillStatusDraft)
			}
		})
	}
}

// TestModelsSkill_StatusFieldNotSettableFromTOML is a direct structural proof
// of invariant (3) above: models.Skill.Status carries the `toml:"-"` tag, so
// even a hand-rolled toml.Unmarshal directly into models.Skill (bypassing
// TOMLSkillWrapper/TOMLSkillDef entirely — the shape ImportFromTOML and
// buildSkillFromTOML both actually decode into) cannot set Status from a
// submitted "status" key. This guards the invariant at the struct-tag layer,
// independent of any particular call site, so a future refactor that decodes
// TOML straight into models.Skill cannot silently reopen the self-promotion
// hole either.
func TestModelsSkill_StatusFieldNotSettableFromTOML(t *testing.T) {
	var s models.Skill
	doc := `
name = "x"
title = "x"
content = "x"
status = "active"
`
	if err := toml.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatalf("toml.Unmarshal: unexpected error: %v", err)
	}
	if s.Status != "" {
		t.Errorf(`Status = %q, want "" (toml:"-" must block Status from ever being set via TOML input)`, s.Status)
	}
}
