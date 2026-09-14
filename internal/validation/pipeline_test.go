package validation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
	"go.uber.org/zap"
)

// newTestPipeline builds a pipeline with a nil store. The tests below only call
// stage helpers that never touch the store (LLMJury, verifySingleResource), so
// the nil store is never dereferenced.
func newTestPipeline(jury ...JuryMember) *Pipeline {
	cfg := config.ValidationConfig{Enabled: true, ApprovalThreshold: 2}
	p := NewPipeline(nil, cfg, zap.NewNop())
	p.jury = jury
	return p
}

// ---------------------------------------------------------------------------
// extractCodeBlocks: pure fenced-code-block extraction from markdown, no I/O.
// ---------------------------------------------------------------------------

func TestExtractCodeBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []codeSnippet
	}{
		{
			name:    "no fenced blocks yields nothing",
			content: "just plain prose with no code",
			want:    nil,
		},
		{
			name: "single fenced block with language tag",
			content: "Some text.\n" +
				"```go\n" +
				"package main\n" +
				"func main() {}\n" +
				"```\n" +
				"More text.",
			want: []codeSnippet{
				{Code: "package main\nfunc main() {}", Language: "go"},
			},
		},
		{
			name: "fenced block with no language tag",
			content: "```\n" +
				"echo hello\n" +
				"```",
			want: []codeSnippet{
				{Code: "echo hello", Language: ""},
			},
		},
		{
			name: "multiple fenced blocks preserve order and languages",
			content: "```python\n" +
				"print('a')\n" +
				"```\n" +
				"text between\n" +
				"```javascript\n" +
				"console.log('b')\n" +
				"```",
			want: []codeSnippet{
				{Code: "print('a')", Language: "python"},
				{Code: "console.log('b')", Language: "javascript"},
			},
		},
		{
			name: "unterminated fenced block is dropped (no closing fence)",
			content: "```go\n" +
				"package main\n",
			want: nil,
		},
		{
			name: "leading/trailing whitespace inside block is trimmed",
			content: "```go\n" +
				"\n\n  package main  \n\n" +
				"```",
			want: []codeSnippet{
				{Code: "package main", Language: "go"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCodeBlocks(tt.content)
			if !codeSnippetsEqual(got, tt.want) {
				t.Errorf("extractCodeBlocks(%q) = %+v, want %+v", tt.content, got, tt.want)
			}
		})
	}
}

func codeSnippetsEqual(a, b []codeSnippet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// G05: an empty jury while validation is enabled is a hard BLOCK, never a pass.
//
// Paired-mutation-real: reverting LLMJury to auto-approve on an empty jury flips
// Consensus to true and fails this test.
// ---------------------------------------------------------------------------

func TestLLMJury_EmptyJuryBlocks(t *testing.T) {
	p := newTestPipeline() // no jurors
	jr, err := p.LLMJury(context.Background(), &models.Skill{Name: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jr.Consensus {
		t.Fatalf("empty jury reached consensus (auto-pass) — must be BLOCKED")
	}
	if len(jr.Votes) != 0 {
		t.Errorf("empty jury has %d votes, want 0", len(jr.Votes))
	}

	// And the stage verdict must be BLOCKED (never PASS).
	st, _, _ := p.juryStage(context.Background(), &models.Skill{Name: "x"})
	if st != StageBlocked {
		t.Errorf("empty-jury stage = %q, want %q", st, StageBlocked)
	}
}

type stubJuror struct {
	approve bool
	err     error
}

func (s stubJuror) ValidateSkill(_ context.Context, _ *models.Skill) (bool, string, error) {
	return s.approve, "stub", s.err
}

// ---------------------------------------------------------------------------
// G05: consensus requires at least two real approvals (floor), even if the
// configured threshold is lower.
// ---------------------------------------------------------------------------

func TestLLMJury_RequiresTwoApprovals(t *testing.T) {
	skill := &models.Skill{Name: "x"}

	// One approving juror, threshold configured at 1 -> still BLOCKED by the >=2 floor.
	one := NewPipeline(nil, config.ValidationConfig{Enabled: true, ApprovalThreshold: 1}, zap.NewNop())
	one.jury = []JuryMember{{Name: "a", LLM: stubJuror{approve: true}}}
	jr, _ := one.LLMJury(context.Background(), skill)
	if jr.Consensus {
		t.Errorf("single approval reached consensus; the >=2 floor must block it")
	}

	// Two approving jurors -> consensus.
	two := NewPipeline(nil, config.ValidationConfig{Enabled: true, ApprovalThreshold: 2}, zap.NewNop())
	two.jury = []JuryMember{
		{Name: "a", LLM: stubJuror{approve: true}},
		{Name: "b", LLM: stubJuror{approve: true}},
	}
	jr, _ = two.LLMJury(context.Background(), skill)
	if !jr.Consensus {
		t.Errorf("two real approvals must reach consensus")
	}

	// One approve, one reject -> only one approval -> no consensus.
	mixed := NewPipeline(nil, config.ValidationConfig{Enabled: true, ApprovalThreshold: 2}, zap.NewNop())
	mixed.jury = []JuryMember{
		{Name: "a", LLM: stubJuror{approve: true}},
		{Name: "b", LLM: stubJuror{approve: false}},
	}
	jr, _ = mixed.LLMJury(context.Background(), skill)
	if jr.Consensus {
		t.Errorf("one approval of two must not reach consensus")
	}
}

// ---------------------------------------------------------------------------
// G21: resource verification is fail-closed and SSRF-guarded.
// ---------------------------------------------------------------------------

// TestVerifySingleResource_BlocksMetadataIP proves the SSRF guard refuses the
// cloud-metadata endpoint. Paired-mutation-real: if isBlockedIP stops flagging
// link-local, the request is not "blocked" and this assertion fails.
func TestVerifySingleResource_BlocksMetadataIP(t *testing.T) {
	p := newTestPipeline()
	res := &models.Resource{
		URL:          "http://169.254.169.254/latest/meta-data/",
		ResourceType: "reference",
	}
	err := p.verifySingleResource(context.Background(), res)
	if err == nil {
		t.Fatalf("expected metadata-IP fetch to be blocked, got nil error")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected an egress-block error, got: %v", err)
	}
}

type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("connection refused (test)")
}

// TestVerifySingleResource_FetchErrorFailsClosed proves a fetch error is a
// verification FAILURE, not a pass. Paired-mutation-real: reverting the GET path
// to "return nil on error" makes this return a nil error and fails the test.
func TestVerifySingleResource_FetchErrorFailsClosed(t *testing.T) {
	p := newTestPipeline()
	p.hostGuard = func(context.Context, string) error { return nil } // allow, isolate the fetch step
	p.httpClient = &http.Client{Transport: errRoundTripper{}}

	res := &models.Resource{URL: "http://example.com/doc", ResourceType: "reference"}
	err := p.verifySingleResource(context.Background(), res)
	if err == nil {
		t.Fatalf("expected fetch error to fail closed, got nil error")
	}
	if !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("expected a fail-closed fetch error, got: %v", err)
	}
}

// TestVerifySingleResource_RequiresHashForTypedResource proves official-doc/code
// resources without a stored hash fail closed (never a HEAD-only pass).
func TestVerifySingleResource_RequiresHashForTypedResource(t *testing.T) {
	p := newTestPipeline()
	res := &models.Resource{URL: "https://example.com/spec", ResourceType: "code", FetchedHash: ""}
	err := p.verifySingleResource(context.Background(), res)
	if err == nil {
		t.Fatalf("expected missing-hash to fail closed, got nil error")
	}
	if !strings.Contains(err.Error(), "hash required") {
		t.Fatalf("expected a hash-required error, got: %v", err)
	}
}
