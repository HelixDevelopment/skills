package codeanalysis

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// G12 test suites — tree-sitter enhancements
//
// Test cases enumerated in research/g12_treesitter_design.md §4:
//   1: unit — compilePatterns(kotlin) produces non-empty pattern map; extraction matches ground truth
//   2: unit — compilePatterns(csharp) produces non-empty pattern map; extraction matches ground truth
//   3: unit — Parse returns ErrNoPatternsForLanguage for unsupported language (never silent)
//   4: unit — Tree.Fidelity is populated on every Parse call, never left blank
//   5: unit — normalizeLanguage aliases work (kt→kotlin, c#→csharp, csharp→csharp)
//   6: integration — native CGO path parses a real Go file (CGO build only)
//   7: integration — regex path parses a real Kotlin file; symbols match ground truth
//   8: integration — regex path parses a real C# file; symbols match ground truth
//   9: fuzz — malformed/truncated source does not panic
//  10: mutation — removing kotlin from compilePatterns makes test #1 fail
//  11: mutation — removing csharp from compilePatterns makes test #2 fail
//  12: mutation — reverting ErrNoPatternsForLanguage makes test #3 fail
//  13: mutation — stubbing a native grammar makes test #6 fail
// ---------------------------------------------------------------------------

// testKotlinSource is a minimal, real Kotlin source fixture with known,
// hand-counted symbols.
const testKotlinSource = `package com.example.demo

import java.util.UUID
import kotlinx.coroutines.*

class GreetingService(private val repo: Repository) {
	fun greet(name: String): String {
		return "Hello, $name!"
	}

	fun asyncGreet(name: String, delay: Long) {
		println("Greeting $name after ${delay}ms")
	}
}

interface Repository {
	fun findById(id: UUID): String?
}

object AppConfig {
	const val APP_NAME = "Demo"
}
`

// testCSharpSource is a minimal, real C# source fixture with known,
// hand-counted symbols.
const testCSharpSource = `using System;
using System.Collections.Generic;

public class GreetingService
{
	private readonly IRepository _repo;

	public GreetingService(IRepository repo)
	{
		_repo = repo;
	}

	public string Greet(string name)
	{
		return $"Hello, {name}!";
	}
}

public interface IRepository
{
	string FindById(string id);
}
`

// ---------------------------------------------------------------------------
// Test 1: kotlin compilePatterns produces non-empty patterns; extraction
// matches hand-verified ground truth
// ---------------------------------------------------------------------------

func TestCompilePatterns_Kotlin_ProducesPatterns(t *testing.T) {
	p := newRegexParser("kotlin")
	if len(p.patterns) == 0 {
		t.Fatal("compilePatterns(kotlin) produced empty pattern map (G12 test #1 RED)")
	}
	for _, key := range []string{"import", "func", "class"} {
		if _, ok := p.patterns[key]; !ok {
			t.Errorf("compilePatterns(kotlin) missing pattern key %q", key)
		}
	}
}

func TestExtract_Kotlin_MatchesGroundTruth(t *testing.T) {
	p := newRegexParser("kotlin")
	parsed := p.Parse([]byte(testKotlinSource))

	// Ground truth for testKotlinSource:
	//   imports: java.util.UUID, kotlinx.coroutines.*  →  2
	//   functions: greet (2x: declaration + single-expression body),
	//              asyncGreet, findById  →  3 (fun names)
	//   classes: GreetingService, Repository, AppConfig  →  3

	if len(parsed.Imports) != 2 {
		t.Errorf("kotlin imports: want 2, got %d", len(parsed.Imports))
	} else {
		// Verify specific imports were found
		imports := make(map[string]bool)
		for _, imp := range parsed.Imports {
			imports[imp.Path] = true
		}
		if !imports["java.util.UUID"] {
			t.Error("expected import java.util.UUID not found")
		}
		if !imports["kotlinx.coroutines.*"] {
			t.Error("expected import kotlinx.coroutines.* not found")
		}
	}

	if len(parsed.Functions) != 3 {
		t.Errorf("kotlin functions: want 3 (greet, asyncGreet, findById), got %d", len(parsed.Functions))
	} else {
		funcs := make(map[string]bool)
		for _, fn := range parsed.Functions {
			funcs[fn.Name] = true
		}
		if !funcs["greet"] {
			t.Error("expected function 'greet' not found")
		}
		if !funcs["asyncGreet"] {
			t.Error("expected function 'asyncGreet' not found")
		}
		if !funcs["findById"] {
			t.Error("expected function 'findById' not found")
		}
	}

	if len(parsed.Classes) != 3 {
		t.Errorf("kotlin classes: want 3 (GreetingService, Repository, AppConfig), got %d", len(parsed.Classes))
	} else {
		classes := make(map[string]bool)
		for _, cls := range parsed.Classes {
			classes[cls.Name] = true
		}
		if !classes["GreetingService"] {
			t.Error("expected class 'GreetingService' not found")
		}
		if !classes["Repository"] {
			t.Error("expected interface 'Repository' not found")
		}
		if !classes["AppConfig"] {
			t.Error("expected object 'AppConfig' not found")
		}
	}
}

// ---------------------------------------------------------------------------
// Test 2: csharp compilePatterns produces non-empty patterns; extraction
// matches hand-verified ground truth
// ---------------------------------------------------------------------------

func TestCompilePatterns_CSharp_ProducesPatterns(t *testing.T) {
	p := newRegexParser("csharp")
	if len(p.patterns) == 0 {
		t.Fatal("compilePatterns(csharp) produced empty pattern map (G12 test #2 RED)")
	}
	for _, key := range []string{"import", "func", "class", "interface"} {
		if _, ok := p.patterns[key]; !ok {
			t.Errorf("compilePatterns(csharp) missing pattern key %q", key)
		}
	}
}

func TestExtract_CSharp_MatchesGroundTruth(t *testing.T) {
	p := newRegexParser("csharp")
	parsed := p.Parse([]byte(testCSharpSource))

	// Ground truth for testCSharpSource:
	//   imports: System, System.Collections.Generic  →  2
	//   functions: Greet (+ constructors may also be captured) → ≥1
	//   classes: GreetingService  →  2 (class + interface)
	//   interfaces: IRepository  →  1

	if len(parsed.Imports) != 2 {
		t.Errorf("csharp imports: want 2, got %d", len(parsed.Imports))
	} else {
		imports := make(map[string]bool)
		for _, imp := range parsed.Imports {
			imports[imp.Path] = true
		}
		if !imports["System"] {
			t.Error("expected import 'System' not found")
		}
		if !imports["System.Collections.Generic"] {
			t.Error("expected import 'System.Collections.Generic' not found")
		}
	}

	if len(parsed.Functions) == 0 {
		t.Errorf("csharp functions: expected at least Greet, got none")
	} else {
		funcs := make(map[string]bool)
		for _, fn := range parsed.Functions {
			funcs[fn.Name] = true
		}
		if !funcs["Greet"] {
			t.Errorf("expected function 'Greet' not found in %v", parsed.Functions)
		}
	}

	if len(parsed.Classes) != 2 {
		t.Errorf("csharp classes: want 2 (GreetingService, IRepository), got %d", len(parsed.Classes))
	} else {
		classes := make(map[string]bool)
		for _, cls := range parsed.Classes {
			classes[cls.Name] = true
		}
		if !classes["GreetingService"] {
			t.Error("expected class 'GreetingService' not found")
		}
		if !classes["IRepository"] {
			t.Error("expected interface 'IRepository' not found (matched as class via pattern)")
		}
	}
}

// ---------------------------------------------------------------------------
// Test 3: Parse returns ErrNoPatternsForLanguage for unsupported lang
// ---------------------------------------------------------------------------

func TestParse_ErrNoPatternsForLanguage(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	// "bash" has no entry in compilePatterns and no native parser
	// (it's registered in NewTreeSitterParser's init list).
	tree, err := parser.Parse([]byte("echo hello"), "bash")
	if !errors.Is(err, ErrNoPatternsForLanguage) {
		t.Fatalf("Parse(bash): want ErrNoPatternsForLanguage, got %v (test #3 RED — silent empty success would be a G12 §2.3 violation)", err)
	}
	if tree != nil {
		t.Fatal("Parse(bash) returned non-nil tree on error")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Tree.Fidelity is populated on every successful Parse call
// ---------------------------------------------------------------------------

func TestParse_FidelityIsSet(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	// When CGO is available, languages with native grammars return FidelityNative;
	// otherwise all return FidelityRegexFallback.
	nativeLangs := map[string]bool{
		"go": true, "python": true, "java": true,
		"javascript": true, "c": true, "cpp": true,
		"rust": true, "csharp": true,
	}

	langs := []struct {
		name   string
		source string
	}{
		{"go", "package main\nfunc main() {}"},
		{"python", "def hello(): pass"},
		{"java", "class Hello {}"},
		{"javascript", "function hello() {}"},
		{"kotlin", "fun hello() {}"},
		{"csharp", "class Hello {}"},
	}

	for _, tt := range langs {
		tree, err := parser.Parse([]byte(tt.source), tt.name)
		if err != nil {
			t.Fatalf("Parse(%s): unexpected error: %v", tt.name, err)
		}

		// Determine expected fidelity
		expectedFid := FidelityRegexFallback
		if cgoAvailable && nativeLangs[tt.name] {
			expectedFid = FidelityNative
		}

		if tree.Fidelity != expectedFid {
			t.Errorf("Parse(%s).Fidelity = %q, want %q (test #4 RED — fidelity must not be blank)", tt.name, tree.Fidelity, expectedFid)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 5: normalizeLanguage aliases for Kotlin/C# are correct
// ---------------------------------------------------------------------------

func TestNormalizeLanguage_KotlinAndCSharp(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"kt", "kotlin"},
		{"c#", "csharp"},
		{"csharp", "csharp"},
		{"C#", "csharp"},
		{"CSHARP", "csharp"},
		{"Kt", "kotlin"},
		{"KOTLIN", "kotlin"},
	}

	for _, tt := range tests {
		got := normalizeLanguage(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 7: Regex path on a real Kotlin file — extracted symbols match
// hand-verified ground truth (integration-level assertion in unit form)
// ---------------------------------------------------------------------------

func TestExtract_Kotlin_RealFixture_GroundTruth(t *testing.T) {
	// This test uses the testKotlinSource fixture declared at package level.
	// It re-asserts the ground truth from Test 1 with the full parser stack
	// (via TreeSitterParser.parseFallback) rather than directly calling
	// RegexParser.Parse, exercising the complete codeanalysis pipeline.
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	tree, err := parser.Parse([]byte(testKotlinSource), "kotlin")
	if err != nil {
		t.Fatalf("Parse(kotlin) via pipeline: %v", err)
	}

	if tree.Fidelity != FidelityRegexFallback {
		t.Errorf("Fidelity: want %q, got %q", FidelityRegexFallback, tree.Fidelity)
	}

	if tree.Parsed == nil {
		t.Fatal("Parsed is nil; expected regex-extracted results")
	}

	// Same ground-truth assertions as TestExtract_Kotlin_MatchesGroundTruth
	if len(tree.Parsed.Imports) != 2 {
		t.Errorf("imports: want 2, got %d", len(tree.Parsed.Imports))
	}
	if len(tree.Parsed.Functions) != 3 {
		t.Errorf("functions: want 3 (greet, asyncGreet, findById), got %d", len(tree.Parsed.Functions))
	}
	if len(tree.Parsed.Classes) != 3 {
		t.Errorf("classes: want 3, got %d", len(tree.Parsed.Classes))
	}
}

// ---------------------------------------------------------------------------
// Test 8: Regex path on a real C# file
// ---------------------------------------------------------------------------

func TestExtract_CSharp_RealFixture_GroundTruth(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	tree, err := parser.Parse([]byte(testCSharpSource), "csharp")
	if err != nil {
		t.Fatalf("Parse(csharp) via pipeline: %v", err)
	}

	// When CGO is available, csharp uses the native parser.
	expectedFid := FidelityRegexFallback
	if cgoAvailable {
		expectedFid = FidelityNative
	}
	if tree.Fidelity != expectedFid {
		t.Errorf("Fidelity: want %q, got %q", expectedFid, tree.Fidelity)
	}

	if tree.Fidelity == FidelityRegexFallback {
		// Regex path: check Parsed field
		if len(tree.Parsed.Imports) != 2 {
			t.Errorf("imports: want 2, got %d", len(tree.Parsed.Imports))
		}
		if len(tree.Parsed.Functions) == 0 {
			t.Errorf("csharp functions: expected at least Greet, got none")
		}
		if len(tree.Parsed.Classes) != 2 {
			t.Errorf("classes: want 2, got %d", len(tree.Parsed.Classes))
		}
	} else {
		// Native path: use ExtractImports/ExtractFunctions/ExtractClasses
		if tree.Root == nil {
			t.Fatal("native path: expected non-nil Root")
		}
		imports, err := parser.ExtractImports(tree, "csharp")
		if err != nil {
			t.Fatalf("ExtractImports: %v", err)
		}
		if len(imports) < 2 {
			t.Errorf("imports: want >=2, got %d", len(imports))
		}
		funcs, err := parser.ExtractFunctions(tree)
		if err != nil {
			t.Fatalf("ExtractFunctions: %v", err)
		}
		if len(funcs) == 0 {
			t.Errorf("csharp functions: expected at least Greet, got none")
		}
		classes, err := parser.ExtractClasses(tree)
		if err != nil {
			t.Fatalf("ExtractClasses: %v", err)
		}
		if len(classes) < 2 {
			t.Errorf("classes: want >=2, got %d", len(classes))
		}
	}
}

// ---------------------------------------------------------------------------
// Test 9: Fuzz — malformed/truncated source does not panic
// ---------------------------------------------------------------------------

func TestFuzz_MalformedSource_NoPanic(t *testing.T) {
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	corruptInputs := []struct {
		name  string
		lang  string
		input []byte
	}{
		{"kotlin truncated", "kotlin", []byte("class Foo\nfun ")},
		{"kotlin single byte", "kotlin", []byte("i")},
		{"kotlin empty", "kotlin", []byte{}},
		{"kotlin binary", "kotlin", []byte{0x00, 0xFF, 0x00, 0x1B}},
		{"csharp truncated", "csharp", []byte("using System;\npublic class ")},
		{"csharp single byte", "csharp", []byte(";")},
		{"csharp empty", "csharp", []byte{}},
		{"csharp random bytes", "csharp", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"go empty", "go", []byte{}},
		{"go binary", "go", []byte{0x00}},
		{"python truncated", "python", []byte("def foo(")},
		{"java null bytes", "java", []byte("class Foo {\n\x00\n}")},
	}

	for _, tc := range corruptInputs {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse(%s) panicked with: %v", tc.lang, r)
				}
			}()

			// We expect ErrNoPatternsForLanguage for unsupported langs in
			// NewTreeSitterParser, or a successful parse for supported ones.
			_, _ = parser.Parse(tc.input, tc.lang)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 6: Native CGO path — only runs when CGO_ENABLED=1
// ---------------------------------------------------------------------------

// TestNativePath_ParsesGoFile asserts that when the cgo build tag is active,
// the native parser path can parse a real Go file and extract symbols matching
// hand-verified ground truth. In non-CGO builds (the common case) it is
// skipped with a clear reason.
func TestNativePath_ParsesGoFile(t *testing.T) {
	if !cgoAvailable {
		t.Skip("CGO not available in this build; native tree-sitter path cannot be exercised (test #6 SKIP-with-reason)")
	}

	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	// Parse a real Go file through the native path
	source := []byte(`package main
import "fmt"
func main() { println("hello") }`)

	tree, err := parser.Parse(source, "go")
	if err != nil {
		t.Fatalf("Parse(go) native: %v", err)
	}

	if tree.Fidelity != FidelityNative {
		t.Errorf("CGO build: expected FidelityNative, got %q", tree.Fidelity)
	}

	if tree.Root == nil {
		t.Fatal("CGO build: expected non-nil Root from native parser")
	}

	// Verify imports were extracted via the extraction API
	imports, err := parser.ExtractImports(tree, "go")
	if err != nil {
		t.Fatalf("ExtractImports: %v", err)
	}
	if len(imports) != 1 {
		t.Errorf("CGO build: expected 1 import, got %d", len(imports))
	}

	// Verify function was extracted
	funcs, err := parser.ExtractFunctions(tree)
	if err != nil {
		t.Fatalf("ExtractFunctions: %v", err)
	}
	if len(funcs) != 1 {
		t.Errorf("CGO build: expected 1 function, got %d", len(funcs))
	} else if funcs[0].Name != "main" {
		t.Errorf("CGO build: expected function 'main', got %q", funcs[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Mutation tests (§1.1 paired mutations)
// ---------------------------------------------------------------------------

// Mutation helpers — these tests prove the original tests are load-bearing.
// They are NOT run in the normal test suite; they are driven by
// scripts/mutation/run_mutation.sh <mutation-id> which applies a surgical
// diff, asserts RED, then restores and asserts GREEN.

// If you remove the "kotlin" case from compilePatterns,
// TestCompilePatterns_Kotlin_ProducesPatterns must fail.
// Mutation ID: mutation-g12-kotlin-compilepatterns
//
// Mutation: delete lines case "kotlin": ... in compilePatterns().
// Expected: TestCompilePatterns_Kotlin_ProducesPatterns → RED (len(p.patterns)==0)

// If you remove the "csharp" case from compilePatterns,
// TestCompilePatterns_CSharp_ProducesPatterns must fail.
// Mutation ID: mutation-g12-csharp-compilepatterns
//
// Mutation: delete lines case "csharp": ... in compilePatterns().
// Expected: TestCompilePatterns_CSharp_ProducesPatterns → RED (len(p.patterns)==0)

// If you revert ErrNoPatternsForLanguage to the old always-nil-error behavior,
// TestParse_ErrNoPatternsForLanguage must fail.
// Mutation ID: mutation-g12-errnopatterns
//
// Mutation: make parseFallback return `return &Tree{...}, nil` instead of
// `return nil, ErrNoPatternsForLanguage` when patterns are empty.
// Expected: TestParse_ErrNoPatternsForLanguage → RED (nil error, nil-equivalent)

// ---------------------------------------------------------------------------
// Regression: NewTreeSitterParser initialises kotlin + csharp support
// ---------------------------------------------------------------------------

func TestNewTreeSitterParser_InitialisesKotlinAndCSharp(t *testing.T) {
	// After the interim regex patterns land (§2.4), NewTreeSitterParser
	// creates RegexParser instances only for the 7 languages in its own
	// init loop. Kotlin/C# are NOT in that loop (they appear only in
	// NewTreeSitterParser's hardcoded lang list for the native-attempt),
	// BUT the G12 fix adds them to compilePatterns, so on-the-fly creation
	// via parseFallback's `newRegexParser(language)` works identically.
	//
	// This test verifies the on-the-fly path succeeds for both.
	parser, err := NewTreeSitterParser()
	if err != nil {
		t.Fatalf("NewTreeSitterParser: %v", err)
	}

	// Parse kotlin — should succeed via on-the-fly newRegexParser
	tree, err := parser.Parse([]byte("fun hello() = println(\"hi\")"), "kotlin")
	if err != nil {
		t.Fatalf("Parse(kotlin) on-the-fly: %v", err)
	}
	if tree.Fidelity != FidelityRegexFallback {
		t.Errorf("Fidelity: want %q, got %q", FidelityRegexFallback, tree.Fidelity)
	}

	// Parse csharp — should succeed (native when CGO available, regex otherwise)
	tree, err = parser.Parse([]byte("class Hello {}"), "csharp")
	if err != nil {
		t.Fatalf("Parse(csharp): %v", err)
	}
	expectedCSharpFid := FidelityRegexFallback
	if cgoAvailable {
		expectedCSharpFid = FidelityNative
	}
	if tree.Fidelity != expectedCSharpFid {
		t.Errorf("Fidelity: want %q, got %q", expectedCSharpFid, tree.Fidelity)
	}
}

// Test that Tree.Fidelity variants are distinct.
func TestFidelity_Constants(t *testing.T) {
	if FidelityNative == FidelityRegexFallback {
		t.Fatal("FidelityNative and FidelityRegexFallback must be distinct constants")
	}
}
