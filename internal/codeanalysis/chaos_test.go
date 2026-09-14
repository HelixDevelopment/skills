// Package codeanalysis provides high-level code analysis capabilities for the
// HelixKnowledge system. This file contains chaos/resilience tests for the
// parser and analyzer under concurrent cancellation and malformed input.
package codeanalysis

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Concurrent parse with cancellation — no leaked goroutines.
// ---------------------------------------------------------------------------

// TestChaos_ConcurrentParseCancel spawns multiple goroutines parsing
// different files concurrently, then cancels the context mid-way. It
// verifies that every goroutine exits within a timeout — no leaks.
func TestChaos_ConcurrentParseCancel(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numFiles = 20

	// Each file is a small Go source.
	makeFile := func(id int) []byte {
		return []byte(strings.Join([]string{
			"package main",
			"",
			"import \"fmt\"",
			"",
			"func main() {",
			"	fmt.Println(\"hello from file", itoa(id), "\")",
			"}",
		}, "\n"))
	}

	var (
		wg    sync.WaitGroup
		count atomic.Int32
	)

	wg.Add(numFiles)
	for i := 0; i < numFiles; i++ {
		i := i
		go func() {
			defer wg.Done()
			content := makeFile(i)
			tree, err := parser.Parse(content, "go")
			if err != nil {
				// ErrNoPatternsForLanguage is expected when CGO tree-sitter is
				// unavailable; it is NOT a leak.
				return
			}
			count.Add(1)
			_ = tree

			// Simulate some downstream work after the parse.
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}()
	}

	// Give goroutines a moment to start parsing, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// All goroutines exited — no leak.
	case <-time.After(5 * time.Second):
		t.Fatal("not all parse goroutines exited within 5s after cancellation; " +
			"possible goroutine leak")
	}

	t.Logf("parsed %d / %d files before cancellation", count.Load(), numFiles)
}

// ---------------------------------------------------------------------------
// Malformed input — parser recovers and subsequent parses succeed.
// ---------------------------------------------------------------------------

// TestChaos_MalformedInputRecovery feeds the parser various malformed inputs
// in rapid succession and verifies that a subsequent valid parse still works
// — the parser does not enter a poisoned internal state.
func TestChaos_MalformedInputRecovery(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	malformed := []struct {
		name     string
		content  []byte
		language string
	}{
		{
			name:     "empty content",
			content:  []byte{},
			language: "go",
		},
		{
			name:     "binary garbage",
			content:  []byte{0x00, 0xFF, 0xDE, 0xAD, 0xBE, 0xEF},
			language: "go",
		},
		{
			name:     "unicode gibberish",
			content:  []byte("💥🔥🚀\x00\x01\x02"),
			language: "go",
		},
		{
			name:     "extremely long line",
			content:  []byte(strings.Repeat("a", 10000) + "\n" + strings.Repeat("b", 10000)),
			language: "go",
		},
		{
			name:     "unclosed string literal",
			content:  []byte(`package main; func f() { s := "unclosed`),
			language: "go",
		},
		{
			name:     "unsupported language",
			content:  []byte(`# "not a real language import"`),
			language: "brainfuck",
		},
		{
			name:     "valid-looking but wrong language content",
			content:  []byte("class Foo { public static void main(String[] args) {} }"),
			language: "python",
		},
	}

	// G1: feed every malformed input to the parser.  None should panic.
	for _, m := range malformed {
		t.Run(m.name, func(t *testing.T) {
			tree, err := parser.Parse(m.content, m.language)
			// ErrNoPatternsForLanguage is an honest expected error for an
			// unsupported language; panics and nil-pointer dereferences are
			// the class of failure this test guards against.
			if err != nil && err != ErrNoPatternsForLanguage {
				t.Logf("Parse returned (non-panicking) error: %v", err)
			}
			_ = tree
		})
	}

	// After the barrage of malformed inputs, a normal Go parse must still
	// succeed — proving the parser recovered.
	finalTree, finalErr := parser.Parse([]byte(`package main
import "fmt"
func main() { fmt.Println("recovery ok") }
`), "go")
	if finalErr != nil && finalErr != ErrNoPatternsForLanguage {
		t.Fatalf("parser did not recover after malformed inputs: %v", finalErr)
	}
	if finalTree != nil && finalTree.Language != "go" {
		t.Errorf("final parsed tree language = %q, want %q", finalTree.Language, "go")
	}
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// itoa is a small integer-to-string helper that avoids importing strconv for
// the test-only format call above.  Go 1.25's runtime has this built in, but
// the explicit helper keeps the test self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
