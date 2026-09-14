// Package codeanalysis provides tree-sitter integration for parsing codebases
// and extracting patterns. The tree-sitter parser is used when CGO is available;
// otherwise, it falls back to regex-based parsing.
package codeanalysis

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNoPatternsForLanguage is returned when the regex-based parser has no
// compiled patterns for a given language. Before this sentinel existed
// (G12, §2.3), an unsupported language silently returned an empty
// FallbackParse, making a Kotlin/C# file indistinguishable from a
// genuinely-empty file.
var ErrNoPatternsForLanguage = errors.New("no regex patterns compiled for language (unsupported or misconfigured)")

// Fidelity describes the parser path that produced a parse result.
type Fidelity string

const (
	// FidelityNative indicates parsing was performed by the CGO-backed
	// tree-sitter native parser (real AST, accurate).
	FidelityNative Fidelity = "native"
	// FidelityRegexFallback indicates parsing was performed by the regex
	// fallback parser (heuristic, reduced accuracy).
	FidelityRegexFallback Fidelity = "regex-fallback"
)

// ---------------------------------------------------------------------------
// TreeSitterParser - wraps tree-sitter Go bindings (CGO) with fallback
// ---------------------------------------------------------------------------

// TreeSitterParser wraps tree-sitter Go bindings for parsing source code.
// When CGO is unavailable, it transparently falls back to regex-based parsing.
type TreeSitterParser struct {
	// nativeParsers holds language-specific native parsers (CGO)
	nativeParsers map[string]bool // language -> available

	// fallbackParsers holds regex-based parsers for non-CGO environments
	fallbackParsers map[string]*RegexParser
}

// Tree represents an abstract syntax tree produced by tree-sitter.
type Tree struct {
	Language string
	Content  []byte
	// Fidelity indicates the parser path used: "native" (CGO tree-sitter)
	// or "regex-fallback" (§2.3, G12). Never left blank for a successfully
	// parsed file.
	Fidelity Fidelity
	// When using native parsing, Root holds the tree-sitter root node.
	// When using fallback, Parsed holds regex-extracted entities.
	Root   *TSNode
	Parsed *FallbackParse
}

// TSNode wraps a tree-sitter node for the native parser path.
type TSNode struct {
	Type      string
	StartByte uint32
	EndByte   uint32
	Children  []*TSNode
	Text      string
}

// FallbackParse holds results from regex-based parsing.
type FallbackParse struct {
	Imports   []Import
	Functions []Function
	Classes   []Class
}

// Function represents a function or method extracted from source code.
type Function struct {
	Name       string
	Signature  string
	Body       string
	File       string
	Line       int
	Language   string
	IsExported bool
}

// Class represents a class, struct, or type definition.
type Class struct {
	Name     string
	Type     string // "class", "struct", "interface", "trait"
	File     string
	Line     int
	Language string
	Methods  []Function
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// NewTreeSitterParser creates a new tree-sitter parser.
// It attempts to initialize native parsers for supported languages,
// falling back to regex-based parsers for any that fail.
func NewTreeSitterParser() (*TreeSitterParser, error) {
	p := &TreeSitterParser{
		nativeParsers:   make(map[string]bool),
		fallbackParsers: make(map[string]*RegexParser),
	}

	// Try to initialize native parsers (CGO path)
	// If CGO is disabled or tree-sitter is not installed,
	// these will silently fail and we'll use regex fallbacks.
	for _, lang := range []string{"go", "python", "java", "javascript", "c", "cpp", "rust", "csharp", "kotlin"} {
		if err := p.initNativeParser(lang); err != nil {
			// Create regex fallback for this language
			p.fallbackParsers[lang] = newRegexParser(lang)
		} else {
			p.nativeParsers[lang] = true
		}
	}

	return p, nil
}

// ---------------------------------------------------------------------------
// Native parser function pointers
//
// These are package-level function variables with default stub implementations
// that return errors/false. When CGO is available, treesitter_native.go's
// init() replaces them with real tree-sitter implementations.
// ---------------------------------------------------------------------------

var doInitNativeParser = func(_ *TreeSitterParser, language string) error {
	return fmt.Errorf("native parser not available for %s (CGO may be disabled)", language)
}

var doParseNative = func(_ *TreeSitterParser, _ []byte, language string) (*Tree, error) {
	return nil, fmt.Errorf("native parser not implemented for %s", language)
}

var doExtractImportsNative = func(_ *TreeSitterParser, _ *Tree) ([]Import, error) {
	return nil, fmt.Errorf("native import extraction not implemented")
}

var doExtractFunctionsNative = func(_ *TreeSitterParser, _ *Tree) ([]Function, error) {
	return nil, fmt.Errorf("native function extraction not implemented")
}

var doExtractClassesNative = func(_ *TreeSitterParser, _ *Tree) ([]Class, error) {
	return nil, fmt.Errorf("native class extraction not implemented")
}

// initNativeParser attempts to initialize a native tree-sitter parser.
// Delegates to doInitNativeParser which is overridden by treesitter_native.go
// when CGO is available.
func (p *TreeSitterParser) initNativeParser(language string) error {
	return doInitNativeParser(p, language)
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// Parse parses source code content and returns an AST representation.
// It uses the native tree-sitter parser when available, otherwise falls
// back to regex-based parsing. Fidelity is always set for a successfully
// parsed result.
func (p *TreeSitterParser) Parse(content []byte, language string) (*Tree, error) {
	language = normalizeLanguage(language)

	// Try native parser first
	if p.nativeParsers[language] {
		tree, err := p.parseNative(content, language)
		if err == nil {
			return tree, nil
		}
		// Native parser failed, fall through to regex
	}

	// Use regex fallback
	return p.parseFallback(content, language)
}

// parseNative uses the tree-sitter C library (CGO required).
// Delegates to doParseNative which is overridden by treesitter_native.go
// when CGO is available.
func (p *TreeSitterParser) parseNative(content []byte, language string) (*Tree, error) {
	return doParseNative(p, content, language)
}

// parseFallback uses regex-based heuristics to extract code structure.
// It sets Fidelity to FidelityRegexFallback and returns
// ErrNoPatternsForLanguage if the regex parser has no compiled patterns
// for the given language (G12, §2.3 — never silent).
func (p *TreeSitterParser) parseFallback(content []byte, language string) (*Tree, error) {
	parser, ok := p.fallbackParsers[language]
	if !ok {
		parser = newRegexParser(language)
	}

	if len(parser.patterns) == 0 {
		return nil, ErrNoPatternsForLanguage
	}

	parsed := parser.Parse(content)

	return &Tree{
		Language: language,
		Content:  content,
		Fidelity: FidelityRegexFallback,
		Parsed:   parsed,
	}, nil
}

// ---------------------------------------------------------------------------
// Extraction methods
// ---------------------------------------------------------------------------

// ExtractImports finds all imports in a parsed source file.
func (p *TreeSitterParser) ExtractImports(tree *Tree, language string) ([]Import, error) {
	// If we have native parse results, use them
	if tree.Root != nil {
		return p.extractImportsNative(tree)
	}

	// Use fallback parse results
	if tree.Parsed != nil {
		return tree.Parsed.Imports, nil
	}

	return nil, fmt.Errorf("no parse results available")
}

// ExtractFunctions finds all function/method definitions in a parsed source file.
func (p *TreeSitterParser) ExtractFunctions(tree *Tree) ([]Function, error) {
	if tree.Root != nil {
		return p.extractFunctionsNative(tree)
	}

	if tree.Parsed != nil {
		return tree.Parsed.Functions, nil
	}

	return nil, fmt.Errorf("no parse results available")
}

// ExtractClasses finds all class/struct/type definitions in a parsed source file.
func (p *TreeSitterParser) ExtractClasses(tree *Tree) ([]Class, error) {
	if tree.Root != nil {
		return p.extractClassesNative(tree)
	}

	if tree.Parsed != nil {
		return tree.Parsed.Classes, nil
	}

	return nil, fmt.Errorf("no parse results available")
}

// ---------------------------------------------------------------------------
// Native extraction (placeholder for CGO implementation)
// ---------------------------------------------------------------------------

func (p *TreeSitterParser) extractImportsNative(tree *Tree) ([]Import, error) {
	return doExtractImportsNative(p, tree)
}

func (p *TreeSitterParser) extractFunctionsNative(tree *Tree) ([]Function, error) {
	return doExtractFunctionsNative(p, tree)
}

func (p *TreeSitterParser) extractClassesNative(tree *Tree) ([]Class, error) {
	return doExtractClassesNative(p, tree)
}

// ---------------------------------------------------------------------------
// Regex-based fallback parser
// ---------------------------------------------------------------------------

// RegexParser provides language-aware regex-based code parsing.
type RegexParser struct {
	language string
	patterns map[string]*regexp.Regexp
}

// newRegexParser creates a regex parser for the given language.
func newRegexParser(language string) *RegexParser {
	p := &RegexParser{
		language: language,
		patterns: make(map[string]*regexp.Regexp),
	}
	p.compilePatterns()
	return p
}

// compilePatterns creates regex patterns for the target language.
func (p *RegexParser) compilePatterns() {
	switch p.language {
	case "go":
		p.patterns["import_single"] = regexp.MustCompile(`(?m)^\s*import\s+"([^"]+)"`)
		p.patterns["import_group"] = regexp.MustCompile(`(?s)import\s*\((.*?)\)`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(([^)]*)\)`)
		p.patterns["struct"] = regexp.MustCompile(`(?m)^\s*type\s+(\w+)\s+struct\s*\{`)
		p.patterns["interface"] = regexp.MustCompile(`(?m)^\s*type\s+(\w+)\s+interface\s*\{`)
		p.patterns["package"] = regexp.MustCompile(`(?m)^\s*package\s+(\w+)`)
	case "python":
		p.patterns["import"] = regexp.MustCompile(`(?m)^\s*(?:from\s+(\S+)\s+)?import\s+(.+)$`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*def\s+(\w+)\s*\(([^)]*)\)`)
		p.patterns["class"] = regexp.MustCompile(`(?m)^\s*class\s+(\w+)\s*(?:\([^)]*\))?\s*:`)
	case "java":
		p.patterns["import"] = regexp.MustCompile(`(?m)^\s*import\s+([^;]+);`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*(?:public|private|protected|static|\s)+\s*(?:[\w<>\[\]]+\s+)+(\w+)\s*\(([^)]*)\)`)
		p.patterns["class"] = regexp.MustCompile(`(?m)^\s*(?:public\s+)?(?:abstract\s+)?class\s+(\w+)`)
		p.patterns["interface"] = regexp.MustCompile(`(?m)^\s*(?:public\s+)?interface\s+(\w+)`)
	case "javascript", "typescript":
		p.patterns["import"] = regexp.MustCompile(`(?m)^\s*import\s+(?:(?:\{[^}]*\}|[^'"]*)\s+from\s+)?['"]([^'"]+)['"]`)
		p.patterns["func"] = regexp.MustCompile(`(?m)(?:function\s+(\w+)|(?:const|let|var)\s+(\w+)\s*=\s*(?:function|\([^)]*\)\s*=>))`)
		p.patterns["class"] = regexp.MustCompile(`(?m)^\s*class\s+(\w+)`)
	case "c", "cpp":
		p.patterns["include"] = regexp.MustCompile(`(?m)^\s*#include\s+["<]([^">]+)[">]`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*(?:[\w*\s]+)\s+(\w+)\s*\(([^)]*)\)\s*\{`)
		p.patterns["struct"] = regexp.MustCompile(`(?m)^\s*(?:typedef\s+)?struct\s+(?:\w+\s+)?\{`)
	case "rust":
		p.patterns["use"] = regexp.MustCompile(`(?m)^\s*use\s+([^;]+);`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?fn\s+(\w+)\s*\(([^)]*)\)`)
		p.patterns["struct"] = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?struct\s+(\w+)`)
		p.patterns["trait"] = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?trait\s+(\w+)`)
	case "kotlin":
		p.patterns["import"] = regexp.MustCompile(`(?m)^\s*import\s+([\w.*]+)`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*(?:fun\s+)(\w+)\s*\(([^)]*)\)`)
		p.patterns["class"] = regexp.MustCompile(`(?m)^\s*(?:class|object|interface)\s+(\w+)`)
	case "csharp":
		p.patterns["import"] = regexp.MustCompile(`(?m)^\s*using\s+([\w.]+)\s*;`)
		p.patterns["func"] = regexp.MustCompile(`(?m)^\s*(?:public|private|protected|internal|static|\s)+(?:[\w<>\[\],\s]+)\s+(\w+)\s*\(([^)]*)\)`)
		p.patterns["class"] = regexp.MustCompile(`(?m)^\s*(?:public\s+)?(?:abstract\s+|sealed\s+)?class\s+(\w+)`)
		p.patterns["interface"] = regexp.MustCompile(`(?m)^\s*(?:public\s+)?interface\s+(\w+)`)
	}
}

// Parse extracts code structure using regex patterns.
func (p *RegexParser) Parse(content []byte) *FallbackParse {
	result := &FallbackParse{}

	// Extract imports
	result.Imports = p.extractImports(content)

	// Extract functions
	result.Functions = p.extractFunctions(content)

	// Extract classes/structs
	result.Classes = p.extractClasses(content)

	return result
}

func (p *RegexParser) extractImports(content []byte) []Import {
	var imports []Import
	lines := bytes.Split(content, []byte("\n"))

	switch p.language {
	case "go":
		// Single imports: import "path"
		for lineNum, line := range lines {
			matches := p.patterns["import_single"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "go",
					})
				}
			}
		}
		// Group imports: import ( ... )
		groupMatches := p.patterns["import_group"].FindAllSubmatch(content, -1)
		for _, m := range groupMatches {
			if len(m) > 1 {
				groupContent := string(m[1])
				for _, line := range strings.Split(groupContent, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "//") {
						continue
					}
					// Remove comments
					if idx := strings.Index(line, "//"); idx >= 0 {
						line = line[:idx]
					}
					line = strings.TrimSpace(line)
					// Extract quoted path
					if start := strings.Index(line, `"`); start >= 0 {
						if end := strings.Index(line[start+1:], `"`); end >= 0 {
							path := line[start+1 : start+1+end]
							imports = append(imports, Import{
								Path:     path,
								Language: "go",
							})
						}
					}
				}
			}
		}
	case "python":
		for lineNum, line := range lines {
			matches := p.patterns["import"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 2 && len(m[1]) > 0 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "python",
					})
				} else if len(m) > 2 {
					// Direct import
					impPaths := strings.Split(string(m[2]), ",")
					for _, imp := range impPaths {
						imp = strings.TrimSpace(imp)
						if imp != "" {
							imports = append(imports, Import{
								Path:     imp,
								Line:     lineNum + 1,
								Language: "python",
							})
						}
					}
				}
			}
		}
	case "java":
		for lineNum, line := range lines {
			matches := p.patterns["import"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "java",
					})
				}
			}
		}
	case "javascript", "typescript":
		for lineNum, line := range lines {
			matches := p.patterns["import"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: p.language,
					})
				}
			}
		}
	case "c", "cpp":
		for lineNum, line := range lines {
			matches := p.patterns["include"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: p.language,
					})
				}
			}
		}
	case "rust":
		for lineNum, line := range lines {
			matches := p.patterns["use"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "rust",
					})
				}
			}
		}
	case "kotlin":
		for lineNum, line := range lines {
			matches := p.patterns["import"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "kotlin",
					})
				}
			}
		}
	case "csharp":
		for lineNum, line := range lines {
			matches := p.patterns["import"].FindAllSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					imports = append(imports, Import{
						Path:     string(m[1]),
						Line:     lineNum + 1,
						Language: "csharp",
					})
				}
			}
		}
	}

	return imports
}

func (p *RegexParser) extractFunctions(content []byte) []Function {
	var functions []Function
	lines := bytes.Split(content, []byte("\n"))

	funcPattern, ok := p.patterns["func"]
	if !ok {
		return functions
	}

	for lineNum, line := range lines {
		matches := funcPattern.FindAllSubmatchIndex(line, -1)
		for _, m := range matches {
			if len(m) >= 4 {
				// Find function name from submatch groups
				var name string
				for i := 2; i < len(m); i += 2 {
					if m[i] >= 0 && m[i+1] >= 0 {
						candidate := string(line[m[i]:m[i+1]])
						if candidate != "" {
							name = candidate
							break
						}
					}
				}
				if name == "" {
					continue
				}

				isExported := false
				if len(name) > 0 {
					firstChar := name[0]
					if p.language == "go" && firstChar >= 'A' && firstChar <= 'Z' {
						isExported = true
					} else if p.language != "go" {
						// For other languages, use convention-based heuristic
						isExported = true
					}
				}

				functions = append(functions, Function{
					Name:       name,
					Line:       lineNum + 1,
					Language:   p.language,
					IsExported: isExported,
				})
			}
		}
	}

	return functions
}

func (p *RegexParser) extractClasses(content []byte) []Class {
	var classes []Class
	lines := bytes.Split(content, []byte("\n"))

	patternKeys := []string{"struct", "class", "interface", "trait"}
	for _, key := range patternKeys {
		pattern, ok := p.patterns[key]
		if !ok {
			continue
		}

		for lineNum, line := range lines {
			matches := pattern.FindAllSubmatch(line, -1)
			for _, m := range matches {
				classType := key
				if key == "struct" {
					classType = "struct"
				}

				var name string
				if len(m) > 1 && len(m[1]) > 0 {
					name = string(m[1])
				} else {
					// Generate anonymous name from line content
					name = fmt.Sprintf("anonymous_%d", lineNum+1)
				}

				classes = append(classes, Class{
					Name:     name,
					Type:     classType,
					Line:     lineNum + 1,
					Language: p.language,
				})
			}
		}
	}

	return classes
}

// ---------------------------------------------------------------------------
// Language utilities
// ---------------------------------------------------------------------------

// normalizeLanguage converts various language name formats to canonical forms.
func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "golang":
		return "go"
	case "py", "python3":
		return "python"
	case "js", "nodejs":
		return "javascript"
	case "ts":
		return "typescript"
	case "c++", "cxx":
		return "cpp"
	case "c#", "csharp":
		return "csharp"
	case "rs":
		return "rust"
	case "kt":
		return "kotlin"
	default:
		return lang
	}
}

// GetSupportedLanguages returns the list of languages the parser supports.
func (p *TreeSitterParser) GetSupportedLanguages() []string {
	langs := make([]string, 0, len(p.nativeParsers)+len(p.fallbackParsers))
	for lang := range p.nativeParsers {
		langs = append(langs, lang)
	}
	for lang := range p.fallbackParsers {
		langs = append(langs, lang)
	}
	return langs
}

// IsLanguageSupported checks if a language can be parsed.
func (p *TreeSitterParser) IsLanguageSupported(language string) bool {
	language = normalizeLanguage(language)
	return p.nativeParsers[language] || p.fallbackParsers[language] != nil
}
