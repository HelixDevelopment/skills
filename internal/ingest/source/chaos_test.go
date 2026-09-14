// Package source defines the addressable-origin abstraction for the skill
// ingestion pipeline. This file contains chaos/resilience tests for
// filesystem source error handling and recovery.
package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestChaos_NonexistentRoot verifies that NewFilesystemSource rejects a
// root directory that does not exist.
func TestChaos_NonexistentRoot(t *testing.T) {
	_, err := NewFilesystemSource("/nonexistent/path/that/does/not/exist", []string{"/tmp"})
	if err == nil {
		t.Error("NewFilesystemSource with nonexistent root should return error")
	}
}

// TestChaos_EmptyAllowedRoots verifies that NewFilesystemSource rejects
// a root when no allowed roots are provided.
func TestChaos_EmptyAllowedRoots(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := NewFilesystemSource(tmpDir, []string{})
	if err == nil {
		t.Error("NewFilesystemSource with empty allowed roots should return error")
	}
}

// TestChaos_RootOutsideAllowed verifies that NewFilesystemSource rejects
// a root that is not inside any allowed root.
func TestChaos_RootOutsideAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	_, err := NewFilesystemSource(tmpDir, []string{otherDir})
	if err == nil {
		t.Error("NewFilesystemSource with root outside allowed should return error")
	}
}

// TestChaos_FetchDeletedFile verifies that Fetch handles a file that was
// deleted between List and Fetch without panicking.
func TestChaos_FetchDeletedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file, list it, delete it, then try to fetch it.
	fpath := filepath.Join(tmpDir, "ephemeral.md")
	if err := os.WriteFile(fpath, []byte("content"), 0644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	fs, err := NewFilesystemSource(tmpDir, []string{tmpDir})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	ctx := context.Background()
	items, err := fs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("List returned 0 items")
	}

	// Delete the file.
	os.Remove(fpath)

	// Fetch should return an error, not panic.
	_, err = fs.Fetch(ctx, items[0])
	if err == nil {
		t.Error("Fetch of deleted file should return error")
	}
}

// TestChaos_FetchPathTraversal verifies that Fetch rejects a ref.Path
// that attempts directory traversal (e.g. "../escape.md").
func TestChaos_FetchPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "safe.md"), []byte("content"), 0644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	fs, err := NewFilesystemSource(tmpDir, []string{tmpDir})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	ctx := context.Background()

	// Attempt path traversal.
	ref := ItemRef{
		SourceID: fs.ID(),
		Path:     "../escape.md",
	}
	_, err = fs.Fetch(ctx, ref)
	if err == nil {
		t.Error("Fetch with path traversal should return error")
	}
}

// TestChaos_WatchOnNonWatchable verifies that Watch returns an error
// (not a panic) when called on a source that reports Watchable() == false.
func TestChaos_WatchOnNonWatchable(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.md"), []byte("content"), 0644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	fs, err := NewFilesystemSource(tmpDir, []string{tmpDir})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	if fs.Watchable() {
		t.Skip("FilesystemSource reports Watchable()=true; Watch test not applicable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan ItemRef, 1)
	err = fs.Watch(ctx, events)
	if err == nil {
		t.Error("Watch on non-watchable source should return error")
	}
}
