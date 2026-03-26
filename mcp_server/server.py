import asyncio
import hashlib
import os
import sys

from mcp.server.fastmcp import FastMCP

from .api_client import api_client


def _encode_path(path: str) -> str:
    """SHA1 hash (first 16 hex chars) of project path for URL routing."""
    return hashlib.sha1(path.encode()).hexdigest()[:16]

mcp = FastMCP("code-index")

_selected_project_path: str | None = os.environ.get("CIX_PROJECT") or None

_NO_PROJECT_MSG = (
    "No project selected. Use select_project with the full project path, "
    "or set the CIX_PROJECT environment variable."
)


def _format_error(e: Exception) -> str:
    if isinstance(e, ConnectionError):
        return str(e)
    return f"Error: {e}"


@mcp.tool()
async def list_projects() -> str:
    """List all indexed projects with their paths and stats."""
    try:
        data = await api_client.get("/api/v1/projects")
        projects = data.get("projects", [])
        if not projects:
            return "No projects registered. Use create_project to add one."

        lines = [f"Found {len(projects)} project(s):\n"]
        for p in projects:
            status_icon = {"indexed": "OK", "indexing": "...", "created": "NEW", "error": "ERR"}.get(p["status"], "?")
            indexed = p.get("last_indexed_at", "never")
            stats = p.get("stats", {})
            lines.append(
                f"  [{status_icon}] {p['host_path']}\n"
                f"       Status: {p['status']} | Files: {stats.get('total_files', 0)} | "
                f"Chunks: {stats.get('total_chunks', 0)} | Symbols: {stats.get('total_symbols', 0)}\n"
                f"       Last indexed: {indexed}"
            )
        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def create_project(path: str) -> str:
    """Register a new codebase. Provide the absolute path to the project root. After creating, use 'cix init' or 'cix reindex' to index."""
    try:
        data = await api_client.post(
            "/api/v1/projects", json={"host_path": path}
        )

        global _selected_project_path
        _selected_project_path = path

        return (
            f"Project created and selected:\n"
            f"Path: {path}\n"
            f"To index, run: cix reindex -p {path}\n"
            f"Or use: cix init {path}"
        )
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def select_project(path: str) -> str:
    """Activate a project for this session. CALL THIS FIRST at the start of each session before using search_code or find_symbols. Provide the full absolute path to the project."""
    try:
        encoded_path = _encode_path(path)

        try:
            project = await api_client.get(f"/api/v1/projects/{encoded_path}")
        except Exception:
            return f"Project at path '{path}' not found. Use create_project to register it first."

        global _selected_project_path
        _selected_project_path = path

        if project["status"] in ("created", "error"):
            return (
                f"Selected project: {path}\n"
                f"Status: {project['status']} — index is not ready.\n"
                f"Run: cix reindex -p {path}"
            )

        if project.get("last_indexed_at"):
            from datetime import datetime, timezone
            try:
                last = datetime.fromisoformat(project["last_indexed_at"])
                now = datetime.now(timezone.utc)
                if (now - last).total_seconds() > 86400:
                    return (
                        f"Selected project: {path}\n"
                        f"Index is stale (>24h). Run: cix reindex -p {path}"
                    )
            except Exception:
                pass

        stats = project.get("stats", {})
        languages = project.get("languages", [])
        return (
            f"Selected project: {path}\n"
            f"Status: {project['status']}\n"
            f"Languages: {', '.join(languages) if languages else 'unknown'}\n"
            f"Files: {stats.get('total_files', 0)} | "
            f"Chunks: {stats.get('total_chunks', 0)} | "
            f"Symbols: {stats.get('total_symbols', 0)}"
        )
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def search_code(query: str, limit: int = 10, file_filter: str = "") -> str:
    """PRIMARY SEARCH TOOL — use this BEFORE Grep/Glob/file reads. Finds code by meaning, not just text. Understands natural language queries like "authentication middleware", "database connection retry logic", "error handling in payment flow". Returns matching code snippets with file paths and line numbers. file_filter is an optional path prefix to narrow scope."""
    if not _selected_project_path:
        return _NO_PROJECT_MSG

    try:
        encoded_path = _encode_path(_selected_project_path)

        body = {"query": query, "limit": limit}
        if file_filter:
            body["paths"] = [file_filter]

        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/search", json=body
        )

        results = data.get("results", [])
        if not results:
            return f"No results found for: {query}"

        lines = [f"Found {data['total']} results for \"{query}\" ({data['query_time_ms']}ms):\n"]
        for i, r in enumerate(results, 1):
            symbol = f"Symbol: {r['symbol_name']} ({r['chunk_type']})" if r.get("symbol_name") else f"Type: {r['chunk_type']}"
            content = r["content"]
            if len(content) > 500:
                content = content[:500] + "\n   ..."
            lines.append(
                f"{i}. [{r['score']:.2f}] {r['file_path']}:{r['start_line']}-{r['end_line']}\n"
                f"   {symbol}\n"
                f"   ```{r.get('language', '')}\n"
                f"   {content}\n"
                f"   ```"
            )
        return "\n\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def find_symbols(query: str, types: list[str] = [], limit: int = 20) -> str:
    """Find functions, classes, methods, or types by name — use this BEFORE Grep when looking for a specific symbol. Faster and more precise than text search. Supports partial names. types filter: "function", "class", "method", "type"."""
    if not _selected_project_path:
        return _NO_PROJECT_MSG

    try:
        encoded_path = _encode_path(_selected_project_path)

        body = {"query": query, "limit": limit}
        if types:
            body["kinds"] = types

        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/search/symbols", json=body
        )

        results = data.get("results", [])
        if not results:
            return f"No symbols found matching: {query}"

        lines = [f"Found {data['total']} symbols matching \"{query}\":\n"]
        for r in results:
            parent = f" (in {r['parent_name']})" if r.get("parent_name") else ""
            sig = f"\n     Signature: {r['signature']}" if r.get("signature") else ""
            lines.append(
                f"  [{r['kind']}] {r['name']}{parent}\n"
                f"     {r['file_path']}:{r['line']}-{r['end_line']} ({r['language']}){sig}"
            )
        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def find_definitions(
    symbol: str,
    kind: str = "",
    file_filter: str = "",
    limit: int = 10,
) -> str:
    """Go to definition — find where a symbol is declared. Use BEFORE Grep when looking for a specific symbol definition. kind filter: function, class, method, type. file_filter narrows to a specific file path."""
    if not _selected_project_path:
        return _NO_PROJECT_MSG

    try:
        encoded_path = _encode_path(_selected_project_path)
        body: dict = {"symbol": symbol, "limit": limit}
        if kind:
            body["kind"] = kind
        if file_filter:
            body["file_path"] = file_filter

        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/search/definitions", json=body
        )

        results = data.get("results", [])
        if not results:
            return f"No definitions found for: {symbol}"

        lines = [f"Found {data['total']} definition(s) for \"{symbol}\":\n"]
        for r in results:
            parent = f" (in {r['parent_name']})" if r.get("parent_name") else ""
            sig = f"\n     Signature: {r['signature']}" if r.get("signature") else ""
            lines.append(
                f"  [{r['kind']}] {r['name']}{parent}\n"
                f"     {r['file_path']}:{r['line']}-{r['end_line']} ({r['language']}){sig}"
            )
        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def find_references(
    symbol: str,
    file_filter: str = "",
    limit: int = 30,
) -> str:
    """Find all usages of a symbol across the codebase (AST-based). Use after find_definitions to trace call sites. Returns file paths and line numbers."""
    if not _selected_project_path:
        return _NO_PROJECT_MSG

    try:
        encoded_path = _encode_path(_selected_project_path)
        body: dict = {"symbol": symbol, "limit": limit}
        if file_filter:
            body["file_path"] = file_filter

        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/search/references", json=body
        )

        results = data.get("results", [])
        if not results:
            return f"No references found for: {symbol}"

        lines = [f"Found {data['total']} reference(s) to \"{symbol}\":\n"]
        for r in results:
            lines.append(f"  {r['file_path']}:{r['start_line']} ({r['language']})")
        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def search_files(pattern: str, limit: int = 20) -> str:
    """Find files by path fragment — use instead of Glob when you know part of a filename or directory name."""
    if not _selected_project_path:
        return _NO_PROJECT_MSG

    try:
        encoded_path = _encode_path(_selected_project_path)
        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/search/files",
            json={"query": pattern, "limit": limit},
        )

        results = data.get("results", [])
        if not results:
            return f"No files found matching: {pattern}"

        total = data.get("total", len(results))
        lines = [f"Found {total} file(s) matching \"{pattern}\":\n"]
        for r in results:
            lang = f" [{r['language']}]" if r.get("language") else ""
            lines.append(f"  {r['file_path']}{lang}")
        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def index_project(path: str = "") -> str:
    """Trigger server-side incremental reindex. Re-embeds files already known to the server. For first-time indexing or after adding new files, use 'cix reindex -p <path>' from the terminal. Defaults to the active project if no path provided."""
    try:
        project_path = path if path else _selected_project_path
        if not project_path:
            return _NO_PROJECT_MSG

        encoded_path = _encode_path(project_path)
        data = await api_client.post(
            f"/api/v1/projects/{encoded_path}/index",
            json={"full": False},
        )

        run_id = data.get("run_id", "unknown")
        message = data.get("message", "Indexing started")
        return (
            f"{message}\n"
            f"Run ID: {run_id}\n"
            f"Use index_status to check progress."
        )
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def index_status(path: str = "") -> str:
    """Check indexing progress. Shows phase, files processed/total, chunks created, and ETA. Defaults to the active project if no path provided."""
    try:
        project_path = path if path else _selected_project_path

        if not project_path:
            return _NO_PROJECT_MSG

        encoded_path = _encode_path(project_path)
        data = await api_client.get(
            f"/api/v1/projects/{encoded_path}/index/status"
        )

        status = data.get("status", "unknown")
        progress = data.get("progress")

        if not progress:
            return f"Indexing status: {status}"

        lines = [f"Indexing status: {status}"]
        if progress.get("phase"):
            lines.append(f"Phase: {progress['phase']}")
        if progress.get("files_total"):
            lines.append(
                f"Files: {progress.get('files_processed', 0)}/{progress['files_total']}"
            )
        if progress.get("chunks_created"):
            lines.append(f"Chunks created: {progress['chunks_created']}")
        if progress.get("elapsed_seconds"):
            lines.append(f"Elapsed: {progress['elapsed_seconds']}s")
        if progress.get("estimated_remaining"):
            lines.append(f"ETA: {progress['estimated_remaining']}s remaining")

        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


@mcp.tool()
async def project_summary(path: str = "") -> str:
    """Get project overview: languages, file counts, top directories, key symbols. Useful to understand project structure before diving into code. Defaults to the active project if no path provided."""
    try:
        project_path = path if path else _selected_project_path

        if not project_path:
            return _NO_PROJECT_MSG

        encoded_path = _encode_path(project_path)
        data = await api_client.get(f"/api/v1/projects/{encoded_path}/summary")

        lines = [
            f"Project: {data['host_path']}",
            f"Status: {data['status']}",
            f"Languages: {', '.join(data.get('languages', []))}",
            f"Files: {data['total_files']} | Chunks: {data['total_chunks']} | Symbols: {data['total_symbols']}",
        ]

        top_dirs = data.get("top_directories", [])
        if top_dirs:
            lines.append("\nTop directories:")
            for d in top_dirs[:7]:
                lines.append(f"  {d['path']} ({d['file_count']} files)")

        symbols = data.get("recent_symbols", [])
        if symbols:
            lines.append("\nKey symbols:")
            for s in symbols[:10]:
                lines.append(f"  [{s['kind']}] {s['name']} ({s['language']})")

        return "\n".join(lines)
    except Exception as e:
        return _format_error(e)


def main():
    mcp.run(transport="stdio")


if __name__ == "__main__":
    main()