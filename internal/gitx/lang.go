package gitx

import (
	"path/filepath"
	"strings"
)

// LangFor maps a path to a highlight.js language id. The client uses it to pick
// a grammar without sniffing content; an empty result means "no highlighting".
func LangFor(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if l, ok := byName[base]; ok {
		return l
	}
	ext := strings.ToLower(filepath.Ext(base))
	return byExt[ext]
}

var byName = map[string]string{
	"dockerfile":     "dockerfile",
	"makefile":       "makefile",
	"cmakelists.txt": "cmake",
	"gemfile":        "ruby",
	"rakefile":       "ruby",
	"go.mod":         "go",
	"go.sum":         "",
	".gitignore":     "bash",
	".env":           "bash",
}

var byExt = map[string]string{
	".go": "go", ".mod": "go",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "typescript", ".mts": "typescript",
	".py": "python", ".pyi": "python",
	".rb": "ruby", ".rs": "rust", ".java": "java", ".kt": "kotlin", ".kts": "kotlin",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".cs": "csharp", ".php": "php", ".swift": "swift", ".m": "objectivec", ".mm": "objectivec",
	".scala": "scala", ".clj": "clojure", ".ex": "elixir", ".exs": "elixir", ".erl": "erlang",
	".hs": "haskell", ".lua": "lua", ".pl": "perl", ".r": "r", ".dart": "dart", ".zig": "zig",
	".sh": "bash", ".bash": "bash", ".zsh": "bash", ".fish": "bash",
	".sql": "sql", ".graphql": "graphql", ".gql": "graphql", ".proto": "protobuf",
	".html": "xml", ".htm": "xml", ".xml": "xml", ".svg": "xml", ".vue": "xml",
	".css": "css", ".scss": "scss", ".sass": "scss", ".less": "less",
	".json": "json", ".jsonc": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "ini", ".ini": "ini",
	".md": "markdown", ".markdown": "markdown", ".rst": "markdown",
	".tf": "terraform", ".hcl": "terraform", ".nix": "nix", ".vim": "vim",
}
