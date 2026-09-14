//go:build cgo

// treesitter_native.go provides the CGO-backed native tree-sitter parser
// implementation. When CGO is enabled and tree-sitter grammars are available,
// this file's init() replaces the default stub function pointers in
// treesitter.go with real implementations.
//
// Supported languages with native grammars: go, python, java, javascript,
// c, cpp, rust, csharp. Languages without Go bindings (kotlin, typescript)
// continue to use the regex fallback.
package codeanalysis

import (
	"fmt"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"

	tree_sitter_csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tree_sitter_cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_js "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

// cgoAvailable is true when CGO is enabled and tree-sitter grammars are
// compiled in. Used by tests to decide whether to skip native-path tests.
var cgoAvailable = true

func init() {
	// Override the default stub function pointers with real CGO implementations.
	doInitNativeParser = nativeInitParser
	doParseNative = nativeParse
	doExtractImportsNative = nativeExtractImports
	doExtractFunctionsNative = nativeExtractFunctions
	doExtractClassesNative = nativeExtractClasses
}

// ---------------------------------------------------------------------------
// Language grammar registry
// ---------------------------------------------------------------------------

// tsLanguage is a minimal adapter over each grammar's Language() function.
type tsLanguage struct {
	lang *sitter.Language
}

// parserPool reuses sitter.Parser instances to avoid repeated allocation.
var parserPool = sync.Pool{
	New: func() any {
		return sitter.NewParser()
	},
}

// nativeLanguages maps canonical language names to their tree-sitter grammar.
// Languages absent from this map will fall back to regex parsing.
var nativeLanguages = map[string]*tsLanguage{
	"go":         {lang: sitter.NewLanguage(tree_sitter_go.Language())},
	"python":     {lang: sitter.NewLanguage(tree_sitter_python.Language())},
	"java":       {lang: sitter.NewLanguage(tree_sitter_java.Language())},
	"javascript": {lang: sitter.NewLanguage(tree_sitter_js.Language())},
	"c":          {lang: sitter.NewLanguage(tree_sitter_c.Language())},
	"cpp":        {lang: sitter.NewLanguage(tree_sitter_cpp.Language())},
	"rust":       {lang: sitter.NewLanguage(tree_sitter_rust.Language())},
	"csharp":     {lang: sitter.NewLanguage(tree_sitter_csharp.Language())},
}

// nativeInitParser checks whether a native grammar is available for the
// given language. Returns nil if the grammar is registered, an error otherwise.
func nativeInitParser(_ *TreeSitterParser, language string) error {
	if _, ok := nativeLanguages[language]; ok {
		return nil
	}
	return fmt.Errorf("native parser not available for %s (no grammar registered)", language)
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// nativeParse parses content using the real tree-sitter CGO parser and
// returns a Tree with FidelityNative.
func nativeParse(_ *TreeSitterParser, content []byte, language string) (*Tree, error) {
	tl, ok := nativeLanguages[language]
	if !ok {
		return nil, fmt.Errorf("no native grammar for %s", language)
	}

	parser := parserPool.Get().(*sitter.Parser)
	defer parserPool.Put(parser)

	if err := parser.SetLanguage(tl.lang); err != nil {
		return nil, fmt.Errorf("set language %s: %w", language, err)
	}

	tsTree := parser.Parse(content, nil)
	if tsTree == nil {
		return nil, fmt.Errorf("tree-sitter returned nil tree for %s", language)
	}
	defer tsTree.Close()

	root := tsTree.RootNode()
	if root == nil {
		return nil, fmt.Errorf("tree-sitter returned nil root for %s", language)
	}

	tsRoot := convertNode(root, content)

	return &Tree{
		Language: language,
		Content:  content,
		Fidelity: FidelityNative,
		Root:     tsRoot,
	}, nil
}

// convertNode recursively converts a sitter.Node into our TSNode wrapper.
func convertNode(n *sitter.Node, content []byte) *TSNode {
	childCount := n.ChildCount()
	children := make([]*TSNode, 0, childCount)
	for i := uint(0); i < childCount; i++ {
		child := n.Child(i)
		if child != nil {
			children = append(children, convertNode(child, content))
		}
	}

	start := n.StartByte()
	end := n.EndByte()
	var text string
	if start <= uint(len(content)) && end <= uint(len(content)) {
		text = string(content[start:end])
	}

	return &TSNode{
		Type:      n.Kind(),
		StartByte: uint32(start),
		EndByte:   uint32(end),
		Children:  children,
		Text:      text,
	}
}

// ---------------------------------------------------------------------------
// Extraction helpers
// ---------------------------------------------------------------------------

// nativeExtractImports walks the native AST and extracts import/include
// statements according to language-specific node types.
func nativeExtractImports(_ *TreeSitterParser, tree *Tree) ([]Import, error) {
	if tree.Root == nil {
		return nil, fmt.Errorf("no native AST available")
	}

	var imports []Import
	walkTree(tree.Root, func(node *TSNode) {
		imp := matchImportNode(node, tree.Language)
		if imp != nil {
			imports = append(imports, *imp)
		}
	})
	return imports, nil
}

// matchImportNode checks whether a TSNode represents an import statement
// for the given language and, if so, extracts the import path.
func matchImportNode(node *TSNode, language string) *Import {
	line := estimateLine(node)
	switch language {
	case "go":
		if node.Type == "import_spec" {
			path := extractQuotedString(node)
			if path != "" {
				return &Import{Path: path, Line: line, Language: language}
			}
		}
	case "python":
		if node.Type == "import_statement" || node.Type == "import_from_statement" {
			text := node.Text
			if text != "" {
				return &Import{Path: text, Line: line, Language: language}
			}
		}
	case "java":
		if node.Type == "import_declaration" {
			text := cleanImportText(node.Text, "import ", ";")
			if text != "" {
				return &Import{Path: text, Line: line, Language: language}
			}
		}
	case "javascript":
		if node.Type == "import_statement" {
			text := node.Text
			if text != "" {
				return &Import{Path: text, Line: line, Language: language}
			}
		}
	case "c", "cpp":
		if node.Type == "preproc_include" {
			path := extractIncludePath(node)
			if path != "" {
				return &Import{Path: path, Line: line, Language: language}
			}
		}
	case "rust":
		if node.Type == "use_declaration" {
			text := cleanImportText(node.Text, "use ", ";")
			if text != "" {
				return &Import{Path: text, Line: line, Language: language}
			}
		}
	case "csharp":
		if node.Type == "using_directive" {
			text := cleanImportText(node.Text, "using ", ";")
			if text != "" {
				return &Import{Path: text, Line: line, Language: language}
			}
		}
	}
	return nil
}

// nativeExtractFunctions walks the native AST and extracts function/method
// definitions.
func nativeExtractFunctions(_ *TreeSitterParser, tree *Tree) ([]Function, error) {
	if tree.Root == nil {
		return nil, fmt.Errorf("no native AST available")
	}

	var funcs []Function
	walkTree(tree.Root, func(node *TSNode) {
		fn := matchFunctionNode(node, tree.Language)
		if fn != nil {
			funcs = append(funcs, *fn)
		}
	})
	return funcs, nil
}

// matchFunctionNode checks whether a TSNode represents a function/method
// definition for the given language.
func matchFunctionNode(node *TSNode, language string) *Function {
	line := estimateLine(node)
	switch language {
	case "go":
		if node.Type == "function_declaration" || node.Type == "method_declaration" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: isGoExported(name)}
			}
		}
	case "python":
		if node.Type == "function_definition" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	case "java":
		if node.Type == "method_declaration" || node.Type == "constructor_declaration" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	case "javascript":
		if node.Type == "function_declaration" || node.Type == "method_definition" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	case "c", "cpp":
		if node.Type == "function_definition" {
			name := findChildText(node, "declarator")
			if name == "" {
				name = extractCFunctionName(node)
			}
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	case "rust":
		if node.Type == "function_item" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	case "csharp":
		if node.Type == "method_declaration" || node.Type == "constructor_declaration" {
			name := findChildText(node, "name")
			if name != "" {
				return &Function{Name: name, Line: line, Language: language, IsExported: true}
			}
		}
	}
	return nil
}

// nativeExtractClasses walks the native AST and extracts class/struct/type
// definitions.
func nativeExtractClasses(_ *TreeSitterParser, tree *Tree) ([]Class, error) {
	if tree.Root == nil {
		return nil, fmt.Errorf("no native AST available")
	}

	var classes []Class
	walkTree(tree.Root, func(node *TSNode) {
		cls := matchClassNode(node, tree.Language)
		if cls != nil {
			classes = append(classes, *cls)
		}
	})
	return classes, nil
}

// matchClassNode checks whether a TSNode represents a class/struct/interface
// definition for the given language.
func matchClassNode(node *TSNode, language string) *Class {
	line := estimateLine(node)
	switch language {
	case "go":
		switch node.Type {
		case "type_declaration":
			name := findTypeSpecName(node)
			if name != "" {
				typeKind := inferGoTypeKind(node)
				return &Class{Name: name, Type: typeKind, Line: line, Language: language}
			}
		}
	case "python":
		if node.Type == "class_definition" {
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "class", Line: line, Language: language}
			}
		}
	case "java":
		switch node.Type {
		case "class_declaration":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "class", Line: line, Language: language}
			}
		case "interface_declaration":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "interface", Line: line, Language: language}
			}
		}
	case "javascript":
		if node.Type == "class_declaration" {
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "class", Line: line, Language: language}
			}
		}
	case "c", "cpp":
		if node.Type == "struct_specifier" || node.Type == "class_specifier" {
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "struct", Line: line, Language: language}
			}
		}
	case "rust":
		switch node.Type {
		case "struct_item":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "struct", Line: line, Language: language}
			}
		case "trait_item":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "trait", Line: line, Language: language}
			}
		case "impl_item":
			name := findChildText(node, "type")
			if name != "" {
				return &Class{Name: name, Type: "impl", Line: line, Language: language}
			}
		}
	case "csharp":
		switch node.Type {
		case "class_declaration":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "class", Line: line, Language: language}
			}
		case "interface_declaration":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "interface", Line: line, Language: language}
			}
		case "struct_declaration":
			name := findChildText(node, "name")
			if name != "" {
				return &Class{Name: name, Type: "struct", Line: line, Language: language}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// AST traversal utilities
// ---------------------------------------------------------------------------

// walkTree performs a depth-first walk of the TSNode tree, calling fn for
// every node (including non-named nodes).
func walkTree(node *TSNode, fn func(*TSNode)) {
	fn(node)
	for _, child := range node.Children {
		walkTree(child, fn)
	}
}

// findChildText returns the Text of the first child whose Type matches the
// given field name, or the first named child's text if the field is not found
// by exact type match.
func findChildText(node *TSNode, fieldType string) string {
	for _, child := range node.Children {
		if child.Type == fieldType && child.Text != "" {
			return child.Text
		}
	}
	// Fallback: search children's children for a "name" or identifier
	for _, child := range node.Children {
		if child.Type == "identifier" || child.Type == "type_identifier" {
			return child.Text
		}
	}
	return ""
}

// extractQuotedString finds the first string literal child and returns its
// content without quotes.
func extractQuotedString(node *TSNode) string {
	for _, child := range node.Children {
		if child.Type == "interpreted_string_literal" || child.Type == "string_literal" || child.Type == "raw_string_literal" {
			text := child.Text
			if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
				return text[1 : len(text)-1]
			}
			return text
		}
	}
	// Fallback: scan text for quotes
	text := node.Text
	if idx := indexOfByte(text, '"'); idx >= 0 {
		if end := indexOfByte(text[idx+1:], '"'); end >= 0 {
			return text[idx+1 : idx+1+end]
		}
	}
	return ""
}

// extractIncludePath extracts the path from a C/C++ #include directive node.
func extractIncludePath(node *TSNode) string {
	for _, child := range node.Children {
		if child.Type == "string_literal" || child.Type == "system_lib_string" {
			text := child.Text
			if len(text) >= 2 {
				// Strip < > or " "
				return text[1 : len(text)-1]
			}
			return text
		}
	}
	return ""
}

// cleanImportText strips a prefix and suffix from import text.
func cleanImportText(text, prefix, suffix string) string {
	t := text
	if len(t) > len(prefix) && t[:len(prefix)] == prefix {
		t = t[len(prefix):]
	}
	if len(t) > len(suffix) && t[len(t)-len(suffix):] == suffix {
		t = t[:len(t)-len(suffix)]
	}
	return trimSpace(t)
}

// findTypeSpecName extracts the type name from a Go type_declaration node.
func findTypeSpecName(node *TSNode) string {
	for _, child := range node.Children {
		if child.Type == "type_spec" {
			return findChildText(child, "name")
		}
	}
	return findChildText(node, "name")
}

// inferGoTypeKind determines whether a Go type_declaration defines a struct,
// interface, or other type.
func inferGoTypeKind(node *TSNode) string {
	for _, child := range node.Children {
		if child.Type == "type_spec" {
			for _, grandchild := range child.Children {
				switch grandchild.Type {
				case "struct_type":
					return "struct"
				case "interface_type":
					return "interface"
				}
			}
		}
	}
	return "type"
}

// extractCFunctionName attempts to extract a function name from a C/C++
// function_definition node by looking for a function_declarator child.
func extractCFunctionName(node *TSNode) string {
	var findDeepestIdentifier func(*TSNode) string
	findDeepestIdentifier = func(n *TSNode) string {
		if n.Type == "identifier" {
			return n.Text
		}
		for _, child := range n.Children {
			if name := findDeepestIdentifier(child); name != "" {
				return name
			}
		}
		return ""
	}

	for _, child := range node.Children {
		if child.Type == "function_declarator" {
			return findDeepestIdentifier(child)
		}
	}
	return ""
}

// estimateLine estimates the 1-based line number of a node by counting
// newlines in its Text prefix. This is an approximation; for exact lines
// the tree-sitter Node.StartPosition().Row would be needed, but that
// requires the tree to remain alive.
func estimateLine(node *TSNode) int {
	line := 1
	for _, b := range node.Text {
		if b == '\n' {
			line++
		}
	}
	return line
}

// isGoExported returns true if a Go identifier starts with an uppercase letter.
func isGoExported(name string) bool {
	if name == "" {
		return false
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}

// indexOfByte returns the index of the first occurrence of c in s, or -1.
func indexOfByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// trimSpace trims whitespace from both ends of a string.
func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
