package discovery

import (
	"path/filepath"
	"strings"
)

// ExtensionMap maps file extensions to language names.
var ExtensionMap = map[string]string{
	".py":     "python",
	".ts":     "typescript",
	".tsx":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".go":     "go",
	".rs":     "rust",
	".java":   "java",
	".c":      "c",
	".h":      "c",
	".cpp":    "cpp",
	".cc":     "cpp",
	".cxx":    "cpp",
	".hpp":    "cpp",
	".cs":     "c_sharp",
	".rb":     "ruby",
	".php":    "php",
	".swift":  "swift",
	".kt":     "kotlin",
	".scala":  "scala",
	".r":      "r",
	".lua":    "lua",
	".sh":     "bash",
	".bash":   "bash",
	".zsh":    "bash",
	".yaml":   "yaml",
	".yml":    "yaml",
	".json":   "json",
	".toml":   "toml",
	".xml":    "xml",
	".html":   "html",
	".css":    "css",
	".scss":   "scss",
	".sql":    "sql",
	".md":     "markdown",
	".rst":    "rst",
	".tf":     "hcl",
	".proto":  "protobuf",
	".dart":   "dart",
	".ex":     "elixir",
	".exs":    "elixir",
	".erl":    "erlang",
	".hs":     "haskell",
	".ml":     "ocaml",
	".vue":    "vue",
	".svelte": "svelte",
}

// DetectLanguage returns the language for a file based on its extension.
// Returns empty string if the language is not recognized.
func DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	return ExtensionMap[ext]
}
