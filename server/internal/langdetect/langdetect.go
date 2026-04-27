// Package langdetect ports api/app/core/language.py to Go.
// It maps file extensions and special filenames to language identifiers
// used throughout the codebase (e.g. "python", "go", "typescript").
package langdetect

import (
	"path/filepath"
	"strings"
)

// extensionMap maps lowercased file extensions to language identifiers.
// Ported 1:1 from EXTENSION_MAP in api/app/core/language.py.
var extensionMap = map[string]string{
	// Systems / compiled
	".py":   "python",
	".go":   "go",
	".rs":   "rust",
	".java": "java",
	".c":    "c",
	".h":    "c",
	".cpp":  "cpp",
	".cc":   "cpp",
	".cxx":  "cpp",
	".hpp":  "cpp",
	".cs":   "c_sharp",
	".swift": "swift",
	".kt":   "kotlin",
	".kts":  "kotlin",
	".scala": "scala",
	".zig":  "zig",
	".jl":   "julia",
	".f90":  "fortran",
	".f95":  "fortran",
	".f03":  "fortran",
	".f":    "fortran",
	".m":    "objc",
	".mm":   "objc",
	// Web / scripting
	".ts":     "typescript",
	".tsx":    "tsx",
	".js":     "javascript",
	".jsx":    "javascript",
	".rb":     "ruby",
	".php":    "php",
	".lua":    "lua",
	".sh":     "bash",
	".bash":   "bash",
	".zsh":    "bash",
	".r":      "r",
	".dart":   "dart",
	".ex":     "elixir",
	".exs":    "elixir",
	".erl":    "erlang",
	".hs":     "haskell",
	".ml":     "ocaml",
	".lisp":   "commonlisp",
	".cl":     "commonlisp",
	".svelte": "svelte",
	// Markup / config / data
	".html":    "html",
	".css":     "css",
	".scss":    "scss",
	".sql":     "sql",
	".yaml":    "yaml",
	".yml":     "yaml",
	".json":    "json",
	".toml":    "toml",
	".xml":     "xml",
	".md":      "markdown",
	".graphql": "graphql",
	".gql":     "graphql",
	".re":      "regex",
	// Infra / build
	".tf":    "hcl",
	".hcl":   "hcl",
	".cmake": "cmake",
}

// filenameMap matches exact filenames (no extension or special names).
// Ported from FILENAME_MAP in api/app/core/language.py.
var filenameMap = map[string]string{
	"CMakeLists.txt": "cmake",
	"Makefile":       "make",
	"GNUmakefile":    "make",
	"Dockerfile":     "dockerfile",
}

// Detect returns the language identifier for a file path, or "" if unknown.
// Mirrors detect_language() in api/app/core/language.py.
func Detect(filePath string) string {
	base := filepath.Base(filePath)

	// Check exact filename first (Makefile, Dockerfile, CMakeLists.txt).
	if lang, ok := filenameMap[base]; ok {
		return lang
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	// Python uses p.suffix.lower() — so ".R" becomes ".r".
	if lang, ok := extensionMap[ext]; ok {
		return lang
	}

	// Special: ".R" → ".r" already handled by ToLower above.
	return ""
}
