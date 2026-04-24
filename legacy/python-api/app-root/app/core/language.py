EXTENSION_MAP: dict[str, str] = {
    # Systems / compiled
    ".py": "python",
    ".go": "go",
    ".rs": "rust",
    ".java": "java",
    ".c": "c",
    ".h": "c",
    ".cpp": "cpp",
    ".cc": "cpp",
    ".cxx": "cpp",
    ".hpp": "cpp",
    ".cs": "c_sharp",
    ".swift": "swift",
    ".kt": "kotlin",
    ".scala": "scala",
    ".zig": "zig",
    ".jl": "julia",
    ".f90": "fortran",
    ".f95": "fortran",
    ".f03": "fortran",
    ".f": "fortran",
    ".m": "objc",
    ".mm": "objc",
    # Web / scripting
    ".ts": "typescript",
    ".tsx": "typescript",
    ".js": "javascript",
    ".jsx": "javascript",
    ".rb": "ruby",
    ".php": "php",
    ".lua": "lua",
    ".sh": "bash",
    ".bash": "bash",
    ".zsh": "bash",
    ".r": "r",
    ".R": "r",
    ".dart": "dart",
    ".ex": "elixir",
    ".exs": "elixir",
    ".erl": "erlang",
    ".hs": "haskell",
    ".ml": "ocaml",
    ".lisp": "commonlisp",
    ".cl": "commonlisp",
    ".svelte": "svelte",
    # Markup / config / data
    ".html": "html",
    ".css": "css",
    ".scss": "scss",
    ".sql": "sql",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".json": "json",
    ".toml": "toml",
    ".xml": "xml",
    ".md": "markdown",
    ".graphql": "graphql",
    ".gql": "graphql",
    ".re": "regex",
    # Infra / build
    ".tf": "hcl",
    ".hcl": "hcl",
    ".cmake": "cmake",
    "CMakeLists.txt": "cmake",
    "Makefile": "make",
    "Dockerfile": "dockerfile",
}

# Filename-based detection (no extension or special names)
FILENAME_MAP: dict[str, str] = {
    "CMakeLists.txt": "cmake",
    "Makefile": "make",
    "GNUmakefile": "make",
    "Dockerfile": "dockerfile",
}


def detect_language(file_path: str) -> str | None:
    from pathlib import Path
    p = Path(file_path)
    # Check filename first (Makefile, Dockerfile, CMakeLists.txt)
    name = p.name
    lang = FILENAME_MAP.get(name)
    if lang:
        return lang
    ext = p.suffix.lower()
    return EXTENSION_MAP.get(ext)
