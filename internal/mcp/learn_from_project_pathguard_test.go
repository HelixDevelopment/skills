package mcp

// ---------------------------------------------------------------------------
// §G31 path-traversal / LFI guard -- WIRING coverage for the LIVE
// learn_from_project MCP tool handler (registerLearnFromProject in
// tools.go). These tests dispatch through the REAL registered tool handler
// via dispatchTool -- the exact code path stdio/HTTP/ACP transports use --
// not a re-implementation, proving:
//
//  1. A malicious project_path (traversal / absolute-outside-root /
//     symlink-escape) is rejected by the LIVE handler BEFORE
//     s.skillStore.SubmitLearningJob is ever reached. Rejection is proven
//     two ways at once: the tool result carries IsError=true, AND the store
//     backing these rejection cases is constructed with a nil *db.Pool --
//     if the guard call in tools.go were ever removed or bypassed, the
//     handler would proceed to SubmitLearningJob's s.pool.Exec call and
//     panic on the nil pool instead of returning a clean error result. A
//     passing test here is only possible because the guard fired first.
//  2. A legitimate in-root project_path is ACCEPTED and the handler
//     proceeds all the way through to a real SubmitLearningJob call against
//     a live throwaway database (SKILL_SYSTEM_TEST_DB_HOST-gated,
//     §11.4.3/§11.4.27 honest skip otherwise) -- "keep legitimate in-root
//     paths working."
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"

	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// newLearnFromProjectTestServer builds a real *MCPServer with tool handlers
// registered (RegisterTools), backed by pool (nil for the rejection cases,
// a live throwaway DB pool for the acceptance case), and codeanalysis.
// allowed_root set to allowedRoot.
func newLearnFromProjectTestServer(t *testing.T, pool *db.Pool, allowedRoot string) *MCPServer {
	t.Helper()

	cfg := &config.Config{
		MCP:        config.MCPConfig{Enabled: true, Transport: "stdio"},
		Validation: config.ValidationConfig{Enabled: false, JurySize: 1, ApprovalThreshold: 1},
		CodeAnalysis: config.CodeAnalysisConfig{
			Enabled:     true,
			AllowedRoot: allowedRoot,
		},
	}

	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	logger := zap.NewNop()

	srv := NewMCPServer(pool, store, reg, cfg, logger)
	srv.RegisterTools()
	return srv
}

// resultText extracts the JSON text body mcp_go.CallToolResult carries in
// its first Content entry (the shape every newToolResult/newToolError call
// in this package produces).
func resultText(t *testing.T, res *mcp_go.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("tool result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(mcp_go.TextContent)
	if !ok {
		t.Fatalf("tool result content[0] is not TextContent: %#v", res.Content[0])
	}
	return tc.Text
}

// ---------------------------------------------------------------------------
// SECURITY (wiring): malicious project_path rejected BEFORE any store/pool
// access -- proven by a nil pool never panicking.
// ---------------------------------------------------------------------------

func TestLearnFromProject_Wiring_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newLearnFromProjectTestServer(t, nil, root)

	traversal := filepath.Join(root, "..", filepath.Base(outside))

	res, err := srv.dispatchTool(context.Background(), "learn_from_project", map[string]interface{}{
		"project_path": traversal,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("learn_from_project(%q) did not report IsError, want rejection; body=%s", traversal, resultText(t, res))
	}
	if body := resultText(t, res); !strings.Contains(body, "Rejected project_path") {
		t.Errorf("error body = %q, want it to mention the rejection", body)
	}
}

func TestLearnFromProject_Wiring_RejectsAbsoluteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newLearnFromProjectTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "learn_from_project", map[string]interface{}{
		"project_path": outside,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("learn_from_project(%q) did not report IsError, want rejection; body=%s", outside, resultText(t, res))
	}
}

func TestLearnFromProject_Wiring_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newLearnFromProjectTestServer(t, nil, root)

	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unsupported in this environment: %v", err)
	}

	res, err := srv.dispatchTool(context.Background(), "learn_from_project", map[string]interface{}{
		"project_path": link,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("learn_from_project(%q) [symlink to outside] did not report IsError, want rejection; body=%s", link, resultText(t, res))
	}
}

func TestLearnFromProject_Wiring_RejectsWhenNoAllowedRootConfigured(t *testing.T) {
	root := t.TempDir() // a path that WOULD be legitimate if a root were set
	srv := newLearnFromProjectTestServer(t, nil, "" /* no allowed root configured */)

	res, err := srv.dispatchTool(context.Background(), "learn_from_project", map[string]interface{}{
		"project_path": root,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("learn_from_project with no allowed_root configured did not report IsError, want fail-closed rejection; body=%s", resultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// UNIT (wiring, live DB): a legitimate in-root project_path is accepted and
// the handler really proceeds to submit the learning job.
// ---------------------------------------------------------------------------

func TestLearnFromProject_Wiring_AcceptsInRootPath_RequiresLiveDatabase(t *testing.T) {
	admin, ok := mcpSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := mcpCreateThrowawayDB(t, admin)
	defer cleanup()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	srv := newLearnFromProjectTestServer(t, pool, root)

	res, err := srv.dispatchTool(context.Background(), "learn_from_project", map[string]interface{}{
		"project_path": child,
		"languages":    []interface{}{"go"},
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if res.IsError {
		t.Fatalf("learn_from_project(%q) [legitimate in-root path] was rejected, want acceptance; body=%s", child, resultText(t, res))
	}

	var payload struct {
		Success     bool   `json:"success"`
		JobID       string `json:"job_id"`
		ProjectPath string `json:"project_path"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &payload); err != nil {
		t.Fatalf("unmarshal tool result body: %v (%s)", err, resultText(t, res))
	}
	if !payload.Success || payload.JobID == "" {
		t.Errorf("payload = %+v, want success=true and a non-empty job_id", payload)
	}

	// The handler must have replaced the raw argument with the CANONICALIZED
	// path (proving the guard's return value -- not merely the raw input --
	// is what actually gets submitted downstream): absolute, and resolving
	// to the same child directory the caller asked for.
	if !filepath.IsAbs(payload.ProjectPath) {
		t.Errorf("payload.ProjectPath = %q, want an absolute canonicalized path", payload.ProjectPath)
	}
	if filepath.Base(payload.ProjectPath) != "child" {
		t.Errorf("payload.ProjectPath = %q, want it to resolve to the %q directory", payload.ProjectPath, "child")
	}

	// Confirm the job was REALLY persisted (audit_log row), not merely
	// reported success without a real store effect.
	var count int
	row := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_log WHERE event = 'learning_job_submitted'")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if count != 1 {
		t.Errorf("audit_log rows for learning_job_submitted = %d, want 1 (the handler must really call SubmitLearningJob on the accept path)", count)
	}
}
