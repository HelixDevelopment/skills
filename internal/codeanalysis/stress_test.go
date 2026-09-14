package codeanalysis

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Stress test for concurrent parser operations.
//
// No database needed — exercises the pure regex-fallback parser against the
// same testKotlinSource and testCSharpSource fixtures used by the existing
// treesitter_test.go cases. N=100 concurrent goroutines parse both sources
// simultaneously. The test asserts no races and no panics.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentParsing(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	const n = 100
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Parse kotlin fixture.
			tree, err := parser.Parse([]byte(testKotlinSource), "kotlin")
			if err != nil {
				t.Errorf("goroutine %d: Parse(kotlin): %v", idx, err)
				return
			}
			if tree == nil {
				t.Errorf("goroutine %d: Parse(kotlin) returned nil tree", idx)
				return
			}
			if tree.Fidelity != FidelityRegexFallback {
				t.Errorf("goroutine %d: Parse(kotlin) Fidelity = %q, want %q",
					idx, tree.Fidelity, FidelityRegexFallback)
			}
			if tree.Parsed == nil {
				t.Errorf("goroutine %d: Parse(kotlin) Parsed is nil", idx)
				return
			}
			if len(tree.Parsed.Imports) != 2 {
				t.Errorf("goroutine %d: kotlin imports = %d, want 2",
					idx, len(tree.Parsed.Imports))
			}
			if len(tree.Parsed.Functions) != 3 {
				t.Errorf("goroutine %d: kotlin functions = %d, want 3",
					idx, len(tree.Parsed.Functions))
			}
			if len(tree.Parsed.Classes) != 3 {
				t.Errorf("goroutine %d: kotlin classes = %d, want 3",
					idx, len(tree.Parsed.Classes))
			}

			// Parse csharp fixture.
			tree, err = parser.Parse([]byte(testCSharpSource), "csharp")
			if err != nil {
				t.Errorf("goroutine %d: Parse(csharp): %v", idx, err)
				return
			}
			if tree == nil {
				t.Errorf("goroutine %d: Parse(csharp) returned nil tree", idx)
				return
			}

			if cgoAvailable {
				// Native path: use extraction API
				if tree.Fidelity != FidelityNative {
					t.Errorf("goroutine %d: Parse(csharp) Fidelity = %q, want %q",
						idx, tree.Fidelity, FidelityNative)
				}
				if tree.Root == nil {
					t.Errorf("goroutine %d: Parse(csharp) Root is nil", idx)
					return
				}
				imports, _ := parser.ExtractImports(tree, "csharp")
				if len(imports) < 2 {
					t.Errorf("goroutine %d: csharp imports = %d, want >=2",
						idx, len(imports))
				}
				funcs, _ := parser.ExtractFunctions(tree)
				if len(funcs) == 0 {
					t.Errorf("goroutine %d: csharp functions = 0, expected at least Greet", idx)
				}
				classes, _ := parser.ExtractClasses(tree)
				if len(classes) < 2 {
					t.Errorf("goroutine %d: csharp classes = %d, want >=2",
						idx, len(classes))
				}
			} else {
				// Regex fallback path
				if tree.Fidelity != FidelityRegexFallback {
					t.Errorf("goroutine %d: Parse(csharp) Fidelity = %q, want %q",
						idx, tree.Fidelity, FidelityRegexFallback)
				}
				if tree.Parsed == nil {
					t.Errorf("goroutine %d: Parse(csharp) Parsed is nil", idx)
					return
				}
				if len(tree.Parsed.Imports) != 2 {
					t.Errorf("goroutine %d: csharp imports = %d, want 2",
						idx, len(tree.Parsed.Imports))
				}
				if len(tree.Parsed.Functions) == 0 {
					t.Errorf("goroutine %d: csharp functions = 0, expected at least Greet", idx)
				}
				if len(tree.Parsed.Classes) != 2 {
					t.Errorf("goroutine %d: csharp classes = %d, want 2",
						idx, len(tree.Parsed.Classes))
				}
			}
		}(i)
	}
	wg.Wait()
}
