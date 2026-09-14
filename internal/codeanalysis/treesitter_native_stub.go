//go:build !cgo

// treesitter_native_stub.go provides stub implementations for the native
// tree-sitter functions when CGO is not available. The default function
// pointers in treesitter.go already return the correct errors/false values,
// so this file only sets cgoAvailable = false.
package codeanalysis

// cgoAvailable is false when CGO is disabled — the regex fallback is the
// only parse path.
var cgoAvailable = false
