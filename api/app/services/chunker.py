import logging
from dataclasses import dataclass

logger = logging.getLogger(__name__)

# Tree-sitter node types to extract per language
LANGUAGE_NODES: dict[str, dict[str, list[str]]] = {
    "python": {
        "function": ["function_definition"],
        "class": ["class_definition"],
    },
    "typescript": {
        "function": ["function_declaration", "arrow_function"],
        "class": ["class_declaration"],
        "method": ["method_definition"],
        "type": ["interface_declaration", "type_alias_declaration"],
    },
    "javascript": {
        "function": ["function_declaration", "arrow_function"],
        "class": ["class_declaration"],
        "method": ["method_definition"],
    },
    "go": {
        "function": ["function_declaration"],
        "method": ["method_declaration"],
        "type": ["type_spec"],
    },
    "rust": {
        "function": ["function_item"],
        "class": ["struct_item", "enum_item"],
        "type": ["trait_item"],
    },
    "java": {
        "function": ["method_declaration"],
        "class": ["class_declaration"],
        "type": ["interface_declaration"],
    },
}

from ..config import settings

MAX_CHUNK_SIZE = settings.max_chunk_tokens * 4  # chars; 1 token ≈ 4 ASCII chars

# Identifier leaf-node types per language (for reference extraction)
IDENTIFIER_NODES: dict[str, set[str]] = {
    "python": {"identifier"},
    "typescript": {"identifier", "type_identifier", "property_identifier"},
    "javascript": {"identifier", "property_identifier"},
    "go": {"identifier", "type_identifier", "field_identifier"},
    "rust": {"identifier", "type_identifier", "field_identifier"},
    "java": {"identifier", "type_identifier"},
}

# Names to skip when extracting references (keywords, builtins, noise)
SKIP_NAMES: set[str] = {
    # Python
    "self", "cls", "None", "True", "False", "print", "len", "range", "type",
    "list", "dict", "set", "tuple", "int", "str", "float", "bool", "bytes",
    "object", "Exception", "isinstance", "hasattr", "getattr", "setattr",
    # JS/TS
    "undefined", "null", "true", "false", "console", "window", "document",
    "Array", "Object", "String", "Number", "Boolean", "Promise", "Map", "Set",
    # Go
    "nil", "fmt", "err", "ctx",
    # Rust
    "Ok", "Err", "Some",
    # Common
    "this", "super", "void",
}

MIN_REF_NAME_LENGTH = 2


@dataclass
class ReferenceInfo:
    name: str
    file_path: str
    line: int      # 1-based
    col: int       # 0-based
    language: str


@dataclass
class ChunkResult:
    chunks: list["CodeChunk"]
    references: list[ReferenceInfo]


@dataclass
class CodeChunk:
    content: str
    chunk_type: str          # function|class|method|type|module|block
    file_path: str
    start_line: int
    end_line: int
    language: str
    symbol_name: str | None
    symbol_signature: str | None
    parent_name: str | None


class ChunkerService:
    def __init__(self):
        self._parsers: dict[str, object] = {}

    def chunk_file(self, file_path: str, content: str, language: str) -> ChunkResult:
        try:
            return self._chunk_with_treesitter(content, language, file_path)
        except Exception as e:
            logger.debug("Tree-sitter failed for %s (%s): %s, falling back to sliding window", file_path, language, e)
            return ChunkResult(
                chunks=self._chunk_sliding_window(content, file_path, language),
                references=[],
            )

    # Map cix language names to (module_name, language_function_name).
    # Each entry corresponds to a PyPI package tree-sitter-<lang>.
    _LANGUAGE_BINDINGS: dict[str, tuple[str, str]] = {
        "python": ("tree_sitter_python", "language"),
        "typescript": ("tree_sitter_typescript", "language_typescript"),
        "javascript": ("tree_sitter_javascript", "language"),
        "go": ("tree_sitter_go", "language"),
        "rust": ("tree_sitter_rust", "language"),
        "java": ("tree_sitter_java", "language"),
        "c": ("tree_sitter_c", "language"),
        "cpp": ("tree_sitter_cpp", "language"),
        "c_sharp": ("tree_sitter_c_sharp", "language"),
        "ruby": ("tree_sitter_ruby", "language"),
        "php": ("tree_sitter_php", "language_php"),
        "swift": ("tree_sitter_swift", "language"),
        "kotlin": ("tree_sitter_kotlin", "language"),
        "scala": ("tree_sitter_scala", "language"),
        "bash": ("tree_sitter_bash", "language"),
        "html": ("tree_sitter_html", "language"),
        "css": ("tree_sitter_css", "language"),
        "scss": ("tree_sitter_scss", "language"),
        "lua": ("tree_sitter_lua", "language"),
        "sql": ("tree_sitter_sql", "language"),
        "json": ("tree_sitter_json", "language"),
        "yaml": ("tree_sitter_yaml", "language"),
        "toml": ("tree_sitter_toml", "language"),
        "xml": ("tree_sitter_xml", "language_xml"),
        "markdown": ("tree_sitter_markdown", "language"),
        "haskell": ("tree_sitter_haskell", "language"),
        "ocaml": ("tree_sitter_ocaml", "language_ocaml"),
        "hcl": ("tree_sitter_hcl", "language"),
        "dart": ("tree_sitter_dart", "language"),
        "elixir": ("tree_sitter_elixir", "language"),
        "erlang": ("tree_sitter_erlang", "language"),
        "zig": ("tree_sitter_zig", "language"),
        "julia": ("tree_sitter_julia", "language"),
        "r": ("tree_sitter_r", "language"),
        "svelte": ("tree_sitter_svelte", "language"),
        "graphql": ("tree_sitter_graphql", "language"),
        "dockerfile": ("tree_sitter_dockerfile", "language"),
        "cmake": ("tree_sitter_cmake", "language"),
        "make": ("tree_sitter_make", "language"),
        "fortran": ("tree_sitter_fortran", "language"),
        "objc": ("tree_sitter_objc", "language"),
        "commonlisp": ("tree_sitter_commonlisp", "language"),
        "regex": ("tree_sitter_regex", "language"),
    }

    def _get_parser(self, language: str):
        if language not in self._parsers:
            try:
                from tree_sitter import Language, Parser
                binding = self._LANGUAGE_BINDINGS.get(language)
                if not binding:
                    return None
                mod_name, func_name = binding
                mod = __import__(mod_name)
                lang = Language(getattr(mod, func_name)())
                self._parsers[language] = Parser(lang)
            except Exception:
                return None
        return self._parsers[language]

    def _chunk_with_treesitter(self, content: str, language: str, file_path: str) -> ChunkResult:
        parser = self._get_parser(language)
        if parser is None:
            return ChunkResult(
                chunks=self._chunk_sliding_window(content, file_path, language),
                references=[],
            )

        tree = parser.parse(content.encode("utf-8"))
        node_types = LANGUAGE_NODES.get(language, {})
        if not node_types:
            return ChunkResult(
                chunks=self._chunk_sliding_window(content, file_path, language),
                references=[],
            )

        # Build flat list of all target node types
        target_types = set()
        type_to_kind: dict[str, str] = {}
        for kind, types in node_types.items():
            for t in types:
                target_types.add(t)
                type_to_kind[t] = kind

        lines = content.split("\n")
        chunks: list[CodeChunk] = []
        covered_ranges: list[tuple[int, int]] = []

        self._extract_nodes(
            tree.root_node, target_types, type_to_kind, lines,
            file_path, language, chunks, covered_ranges, parent_name=None,
        )

        # Extract references from AST
        references = self._extract_references(
            tree.root_node, target_types, file_path, language,
        )

        # Collect gaps as module chunks
        covered_ranges.sort()
        gap_lines = self._find_gaps(covered_ranges, len(lines))
        for start, end in gap_lines:
            gap_content = "\n".join(lines[start:end + 1]).strip()
            if gap_content:
                chunks.append(CodeChunk(
                    content=gap_content,
                    chunk_type="module",
                    file_path=file_path,
                    start_line=start + 1,
                    end_line=end + 1,
                    language=language,
                    symbol_name=None,
                    symbol_signature=None,
                    parent_name=None,
                ))

        # Split oversized chunks
        final_chunks = []
        for chunk in chunks:
            if len(chunk.content) > MAX_CHUNK_SIZE:
                final_chunks.extend(self._split_chunk(chunk))
            else:
                final_chunks.append(chunk)

        if not final_chunks:
            return ChunkResult(
                chunks=self._chunk_sliding_window(content, file_path, language),
                references=[],
            )

        return ChunkResult(chunks=final_chunks, references=references)

    def _extract_nodes(
        self, node, target_types, type_to_kind, lines,
        file_path, language, chunks, covered_ranges, parent_name,
    ):
        if node.type in target_types:
            start_line = node.start_point[0]
            end_line = node.end_point[0]
            content = "\n".join(lines[start_line:end_line + 1])
            kind = type_to_kind[node.type]

            # Detect if this is a method (function inside a class)
            actual_kind = kind
            if kind == "function" and parent_name is not None:
                actual_kind = "method"

            # Extract symbol name
            symbol_name = self._extract_name(node)

            # Extract signature (first line)
            signature = lines[start_line].strip() if start_line < len(lines) else None

            chunks.append(CodeChunk(
                content=content,
                chunk_type=actual_kind,
                file_path=file_path,
                start_line=start_line + 1,
                end_line=end_line + 1,
                language=language,
                symbol_name=symbol_name,
                symbol_signature=signature,
                parent_name=parent_name,
            ))
            covered_ranges.append((start_line, end_line))

            # For classes, recurse with class name as parent
            if kind == "class":
                current_parent = symbol_name or parent_name
                for child in node.children:
                    self._extract_nodes(
                        child, target_types, type_to_kind, lines,
                        file_path, language, chunks, covered_ranges,
                        parent_name=current_parent,
                    )
                return

        for child in node.children:
            self._extract_nodes(
                child, target_types, type_to_kind, lines,
                file_path, language, chunks, covered_ranges,
                parent_name=parent_name,
            )

    def _extract_references(
        self, root_node, target_types: set, file_path: str, language: str,
    ) -> list[ReferenceInfo]:
        """Walk AST and collect identifier nodes that are usages (not definitions)."""
        id_node_types = IDENTIFIER_NODES.get(language)
        if not id_node_types:
            return []

        refs: list[ReferenceInfo] = []
        seen: set[tuple[str, int, int]] = set()

        def _walk(node):
            if node.type in id_node_types:
                name = node.text.decode("utf-8") if isinstance(node.text, bytes) else node.text
                if (
                    name
                    and len(name) >= MIN_REF_NAME_LENGTH
                    and name not in SKIP_NAMES
                ):
                    # Skip if this identifier is the name child of a definition node
                    parent = node.parent
                    if parent and parent.type in target_types:
                        # Check if this is the "name" child (first identifier)
                        is_def_name = False
                        for child in parent.children:
                            if child.type in id_node_types:
                                is_def_name = (child.id == node.id)
                                break
                        if is_def_name:
                            return

                    line = node.start_point[0] + 1  # 1-based
                    col = node.start_point[1]        # 0-based
                    key = (name, line, col)
                    if key not in seen:
                        seen.add(key)
                        refs.append(ReferenceInfo(
                            name=name,
                            file_path=file_path,
                            line=line,
                            col=col,
                            language=language,
                        ))
                return  # leaf node, no children to recurse

            for child in node.children:
                _walk(child)

        _walk(root_node)
        return refs

    @staticmethod
    def _extract_name(node) -> str | None:
        for child in node.children:
            if child.type in ("identifier", "name", "property_identifier", "type_identifier"):
                return child.text.decode("utf-8") if isinstance(child.text, bytes) else child.text
        return None

    @staticmethod
    def _find_gaps(covered: list[tuple[int, int]], total_lines: int) -> list[tuple[int, int]]:
        if not covered:
            return [(0, total_lines - 1)] if total_lines > 0 else []

        gaps = []
        prev_end = -1
        for start, end in covered:
            if start > prev_end + 1:
                gaps.append((prev_end + 1, start - 1))
            prev_end = max(prev_end, end)
        if prev_end < total_lines - 1:
            gaps.append((prev_end + 1, total_lines - 1))
        return gaps

    @staticmethod
    def _split_chunk(chunk: CodeChunk) -> list[CodeChunk]:
        lines = chunk.content.split("\n")
        sub_chunks = []
        current_lines = []
        current_start = chunk.start_line

        for i, line in enumerate(lines):
            current_lines.append(line)
            current_content = "\n".join(current_lines)
            if len(current_content) >= MAX_CHUNK_SIZE and len(current_lines) > 1:
                # Split here
                split_content = "\n".join(current_lines[:-1])
                sub_chunks.append(CodeChunk(
                    content=split_content,
                    chunk_type=chunk.chunk_type,
                    file_path=chunk.file_path,
                    start_line=current_start,
                    end_line=current_start + len(current_lines) - 2,
                    language=chunk.language,
                    symbol_name=chunk.symbol_name,
                    symbol_signature=chunk.symbol_signature,
                    parent_name=chunk.parent_name,
                ))
                current_start = current_start + len(current_lines) - 1
                current_lines = [line]

        if current_lines:
            sub_chunks.append(CodeChunk(
                content="\n".join(current_lines),
                chunk_type=chunk.chunk_type,
                file_path=chunk.file_path,
                start_line=current_start,
                end_line=chunk.end_line,
                language=chunk.language,
                symbol_name=chunk.symbol_name,
                symbol_signature=chunk.symbol_signature,
                parent_name=chunk.parent_name,
            ))

        return sub_chunks

    def _chunk_sliding_window(self, content: str, file_path: str, language: str) -> list[CodeChunk]:
        window_size = 4000  # chars (~1000 tokens)
        overlap = 500       # chars (~125 tokens)
        chunks = []

        lines = content.split("\n")
        current_pos = 0
        chunk_start_line = 0

        while current_pos < len(content):
            end_pos = min(current_pos + window_size, len(content))
            chunk_content = content[current_pos:end_pos]

            # Count lines
            start_line = content[:current_pos].count("\n")
            end_line = content[:end_pos].count("\n")

            chunks.append(CodeChunk(
                content=chunk_content,
                chunk_type="block",
                file_path=file_path,
                start_line=start_line + 1,
                end_line=end_line + 1,
                language=language,
                symbol_name=None,
                symbol_signature=None,
                parent_name=None,
            ))

            if end_pos >= len(content):
                break
            current_pos = end_pos - overlap

        return chunks


chunker_service = ChunkerService()
