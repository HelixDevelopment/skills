package source

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const fixtureRoot = "testdata/docs"

func absFixtureRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(fixtureRoot)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", fixtureRoot, err)
	}
	return abs
}

// buildTestTreeWithGitDir copies the fixtureRoot tree into a fresh
// t.TempDir() and adds a synthetic ".git/HEAD" file there AT TEST TIME.
//
// A ".git" path component can never be a STATIC, committed testdata
// fixture: `git add` silently OMITS any path containing a ".git"
// directory segment (verified via `git add -n` over this package's
// tree), so a committed "testdata/docs/.git/HEAD" file would vanish on
// a fresh clone -- the tests that depend on it would either fail (the
// positive case, which lists it) or become vacuous (the exclusion case,
// which would "pass" with nothing to exclude). Building the fixture at
// runtime, in a throwaway temp dir, keeps the exact same test shape
// without ever depending on a file git cannot track (§11.4.108).
func buildTestTreeWithGitDir(t *testing.T) string {
	t.Helper()
	src := absFixtureRoot(t)
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture tree %q -> %q: %v", src, dst, err)
	}

	gitDir := filepath.Join(dst, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", filepath.Join(gitDir, "HEAD"), err)
	}
	return dst
}

// ---------------------------------------------------------------------------
// Happy path + generic Source contract (against the REAL filesystem, real
// fixture files under testdata/ -- no network, no DB, fully offline).
// ---------------------------------------------------------------------------

func TestFilesystemSource_ListFetch_ContractAndShape(t *testing.T) {
	root := absFixtureRoot(t)
	src, err := NewFilesystemSource(root, []string{root}, WithExcludePatterns([]string{".git"}))
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	var _ Source = src // compile-time contract check

	wantPaths := []string{"sample.md", "plain_notes.txt", "unsupported_page.html", filepath.ToSlash(filepath.Join("nested", "child_note.md"))}
	assertSourceContract(t, src, wantPaths)

	if got, want := src.ID(), "fs:"+root; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}

	refs, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var sampleRef ItemRef
	found := false
	for _, r := range refs {
		if r.Path == "sample.md" {
			sampleRef, found = r, true
		}
	}
	if !found {
		t.Fatalf("List did not return sample.md; got %+v", refs)
	}

	raw, err := src.Fetch(context.Background(), sampleRef)
	if err != nil {
		t.Fatalf("Fetch(sample.md): %v", err)
	}
	if raw.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", raw.ContentType)
	}
	wantBody, err := os.ReadFile(filepath.Join(root, "sample.md"))
	if err != nil {
		t.Fatalf("read fixture directly: %v", err)
	}
	if !bytes.Equal(raw.Body, wantBody) {
		t.Errorf("Fetch body does not match fixture bytes exactly")
	}
}

func TestFilesystemSource_List_ExcludesGitDir(t *testing.T) {
	root := buildTestTreeWithGitDir(t)
	src, err := NewFilesystemSource(root, []string{root}, WithExcludePatterns([]string{".git"}))
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	refs, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range refs {
		if filepath.Base(filepath.Dir(r.Path)) == ".git" || r.Path == ".git/HEAD" {
			t.Fatalf("List returned an item under the excluded .git dir: %q", r.Path)
		}
	}
}

func TestFilesystemSource_List_WithoutExcludePattern_IncludesGitDir(t *testing.T) {
	// Negative control (§11.4.201): proves the exclusion in the previous
	// test is caused by the configured pattern, not by some unrelated
	// default behavior silently skipping dotfiles/dot-dirs.
	root := buildTestTreeWithGitDir(t)
	src, err := NewFilesystemSource(root, []string{root})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	refs, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sawGit := false
	for _, r := range refs {
		if r.Path == filepath.ToSlash(filepath.Join(".git", "HEAD")) {
			sawGit = true
		}
	}
	if !sawGit {
		t.Fatalf("without an exclude pattern, .git/HEAD should be listed; got %+v", refs)
	}
}

func TestFilesystemSource_List_VanishedRoot_ReturnsErrorNotEmptyList(t *testing.T) {
	// A root that vanishes (or becomes unreadable) between construction
	// and List() must be reported as an ERROR, never conflated with "an
	// empty, readable root with zero items" -- a false-empty result here
	// would silently hide a broken source (§11.4.201).
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", root, err)
	}

	src, err := NewFilesystemSource(root, []string{root})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll(%q): %v", root, err)
	}

	if _, err := src.List(context.Background()); err == nil {
		t.Fatal("List on a vanished root = nil error, want a propagated error")
	}
}

// ---------------------------------------------------------------------------
// Allowlist / traversal / symlink-escape guard: mirrors
// internal/codeanalysis/pathguard_test.go's matrix, since
// NewFilesystemSource calls the SAME codeanalysis.ValidateProjectPath
// function rather than reimplementing the guard.
// ---------------------------------------------------------------------------

func TestFilesystemSource_New_EmptyAllowedRoots_FailsClosed(t *testing.T) {
	root := absFixtureRoot(t)
	_, err := NewFilesystemSource(root, nil)
	if err == nil {
		t.Fatal("NewFilesystemSource with empty allowedRoots = nil error, want fail-closed rejection")
	}
}

func TestFilesystemSource_New_RootOutsideAllowedRoots_Rejected(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	outside := filepath.Join(tmp, "outside")
	for _, d := range []string{allowed, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	_, err := NewFilesystemSource(outside, []string{allowed})
	if err == nil {
		t.Fatal("NewFilesystemSource(outside, [allowed]) = nil error, want rejection")
	}
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("error = %v, want it to wrap ErrPathNotAllowed", err)
	}
}

func TestFilesystemSource_New_TraversalEscape_Rejected(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	outside := filepath.Join(tmp, "outside")
	for _, d := range []string{allowed, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	traversal := filepath.Join(allowed, "..", "outside")
	_, err := NewFilesystemSource(traversal, []string{allowed})
	if err == nil {
		t.Fatal("NewFilesystemSource with a traversal root = nil error, want rejection")
	}
}

func TestFilesystemSource_New_RootIsAllowedRootItself_Accepted(t *testing.T) {
	root := absFixtureRoot(t)
	src, err := NewFilesystemSource(root, []string{root})
	if err != nil {
		t.Fatalf("NewFilesystemSource(root, [root]) should be accepted: %v", err)
	}
	if src == nil {
		t.Fatal("NewFilesystemSource returned nil source with nil error")
	}
}

func TestFilesystemSource_Fetch_PathEscapingRoot_Rejected(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	outside := filepath.Join(tmp, "outside")
	for _, d := range []string{allowed, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	// Plant a REAL file OUTSIDE the allowed root. An earlier version of
	// this test targeted "../../../../etc/passwd", which -- from a deep
	// fixture path -- resolves to a NONEXISTENT location on most hosts;
	// an unguarded Fetch would ALSO fail there (a plain os.Stat ENOENT),
	// so the test stayed green even when the traversal guard itself was
	// removed (verified by mutation: removing the
	// codeanalysis.ValidateProjectPath call in filesystem.go's Fetch left
	// this exact test passing). Targeting a file that genuinely EXISTS
	// outside the allowed root means the test can only pass if the guard
	// itself rejects the escape (§11.4.125(6)/§11.4.194).
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", secret, err)
	}

	src, err := NewFilesystemSource(allowed, []string{allowed})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	// A ref whose Path lexically escapes root via ".." even though it
	// carries this source's own SourceID -- Fetch must still reject it
	// via the re-validated canonical-path boundary check, not merely
	// trust a source-relative-looking Path.
	escaping := ItemRef{SourceID: src.ID(), Path: "../outside/secret.txt"}
	_, err = src.Fetch(context.Background(), escaping)
	if err == nil {
		t.Fatal("Fetch with a traversal Path = nil error, want rejection")
	}
	if !errors.Is(err, ErrFetchPathRejected) {
		t.Errorf("error = %v, want it to wrap ErrFetchPathRejected", err)
	}
}

// ---------------------------------------------------------------------------
// Size cap: exactly-at-cap accepted, one byte over cleanly rejected
// (never truncated -- DESIGN.md §6.4 boundary-input requirement).
// ---------------------------------------------------------------------------

func writeExactSizeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	content := bytes.Repeat([]byte{'x'}, size)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", p, err)
	}
	return p
}

func TestFilesystemSource_Fetch_ExactlyAtSizeCap_Accepted(t *testing.T) {
	tmp := t.TempDir()
	const capSize = 128
	writeExactSizeFile(t, tmp, "at_cap.txt", capSize)

	src, err := NewFilesystemSource(tmp, []string{tmp}, WithMaxItemSizeBytes(int64(capSize)))
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	raw, err := src.Fetch(context.Background(), ItemRef{SourceID: src.ID(), Path: "at_cap.txt"})
	if err != nil {
		t.Fatalf("Fetch at exactly the cap should succeed: %v", err)
	}
	if len(raw.Body) != capSize {
		t.Errorf("len(Body) = %d, want %d (must not truncate)", len(raw.Body), capSize)
	}
}

func TestFilesystemSource_Fetch_OneByteOverSizeCap_RejectedCleanly(t *testing.T) {
	tmp := t.TempDir()
	const capSize = 128
	writeExactSizeFile(t, tmp, "over_cap.txt", capSize+1)

	src, err := NewFilesystemSource(tmp, []string{tmp}, WithMaxItemSizeBytes(int64(capSize)))
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	_, err = src.Fetch(context.Background(), ItemRef{SourceID: src.ID(), Path: "over_cap.txt"})
	if err == nil {
		t.Fatal("Fetch one byte over the cap = nil error, want a clean rejection")
	}
	if !errors.Is(err, ErrOverMaxItemSize) {
		t.Errorf("error = %v, want it to wrap ErrOverMaxItemSize", err)
	}
}

func TestFilesystemSource_Fetch_ZeroByteFile_Accepted(t *testing.T) {
	// Boundary input: empty file (DESIGN.md §6.4).
	tmp := t.TempDir()
	writeExactSizeFile(t, tmp, "empty.txt", 0)

	src, err := NewFilesystemSource(tmp, []string{tmp})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	raw, err := src.Fetch(context.Background(), ItemRef{SourceID: src.ID(), Path: "empty.txt"})
	if err != nil {
		t.Fatalf("Fetch zero-byte file: %v", err)
	}
	if len(raw.Body) != 0 {
		t.Errorf("len(Body) = %d, want 0", len(raw.Body))
	}
}

// ---------------------------------------------------------------------------
// Watchable/Watch: this increment always returns false/error (see
// filesystem.go type-level doc comment on the §11.4.6 discrepancy vs.
// DESIGN.md).
// ---------------------------------------------------------------------------

func TestFilesystemSource_NotWatchableInThisIncrement(t *testing.T) {
	root := absFixtureRoot(t)
	src, err := NewFilesystemSource(root, []string{root})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}
	if src.Watchable() {
		t.Fatal("Watchable() = true, want false (real-time watch is a separate, later item: F3.1)")
	}
	if err := src.Watch(context.Background(), make(chan ItemRef)); err == nil {
		t.Fatal("Watch() = nil error, want an explicit not-implemented error")
	}
}

// ---------------------------------------------------------------------------
// Context cancellation is honored.
// ---------------------------------------------------------------------------

func TestFilesystemSource_Fetch_CancelledContext(t *testing.T) {
	root := absFixtureRoot(t)
	src, err := NewFilesystemSource(root, []string{root})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = src.Fetch(ctx, ItemRef{SourceID: src.ID(), Path: "sample.md"})
	if err == nil {
		t.Fatal("Fetch with a cancelled context = nil error, want ctx.Err()")
	}
}
