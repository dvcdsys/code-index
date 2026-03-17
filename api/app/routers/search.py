import json
import time
from collections import Counter
from pathlib import Path
from ..core.path_encoding import resolve_project_path

from fastapi import APIRouter, Depends

from ..auth import verify_api_key
from ..core.exceptions import ProjectNotFoundError
from ..database import get_db
from ..schemas.search import (
    DefinitionItem,
    DefinitionRequest,
    DefinitionResponse,
    FileResultItem,
    FileSearchRequest,
    FileSearchResponse,
    ProjectSummary,
    ReferenceItem,
    ReferenceRequest,
    ReferenceResponse,
    SearchRequest,
    SearchResponse,
    SearchResultItem,
    SymbolResultItem,
    SymbolSearchRequest,
    SymbolSearchResponse,
)
from ..services.embeddings import embedding_service
from ..services.symbol_index import symbol_index_service
from ..services.vector_store import vector_store_service

router = APIRouter(
    prefix="/api/v1/projects",
    dependencies=[Depends(verify_api_key)],
)


async def _get_project(project_path: str):
    db = await get_db()
    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    if not row:
        raise ProjectNotFoundError(project_path)
    return row


@router.post("/{project_path}/search", response_model=SearchResponse)
async def semantic_search(project_path: str, body: SearchRequest):
    project_path = await resolve_project_path(project_path)
    await _get_project(project_path)
    start = time.time()

    query_embedding = await embedding_service.embed_query(body.query)

    where = {}
    if body.languages:
        if len(body.languages) == 1:
            where["language"] = body.languages[0]
        else:
            where["$or"] = [{"language": lang} for lang in body.languages]

    results = await vector_store_service.search(
        project_path, query_embedding, limit=body.limit * 2, where=where or None,
    )

    # Filter by min_score and path patterns
    filtered = []
    for r in results:
        if r["score"] < body.min_score:
            continue
        if body.paths:
            if not any(r["file_path"].startswith(p) or p in r["file_path"] for p in body.paths):
                continue
        filtered.append(r)

    filtered = filtered[:body.limit]
    elapsed = (time.time() - start) * 1000

    return SearchResponse(
        results=[SearchResultItem(**r) for r in filtered],
        total=len(filtered),
        query_time_ms=round(elapsed, 1),
    )


@router.post("/{project_path}/search/symbols", response_model=SymbolSearchResponse)
async def symbol_search(project_path: str, body: SymbolSearchRequest):
    project_path = await resolve_project_path(project_path)
    await _get_project(project_path)

    symbols = await symbol_index_service.search(
        project_path, body.query, kinds=body.kinds or None, limit=body.limit,
    )

    results = [
        SymbolResultItem(
            name=s.name,
            kind=s.kind,
            file_path=s.file_path,
            line=s.line,
            end_line=s.end_line,
            language=s.language,
            signature=s.signature,
            parent_name=s.parent_name,
        )
        for s in symbols
    ]

    return SymbolSearchResponse(results=results, total=len(results))


@router.post("/{project_path}/search/files", response_model=FileSearchResponse)
async def file_search(project_path: str, body: FileSearchRequest):
    project_path = await resolve_project_path(project_path)
    await _get_project(project_path)

    db = await get_db()
    cursor = await db.execute(
        "SELECT file_path FROM file_hashes WHERE project_path = ? AND file_path LIKE ?",
        (project_path, f"%{body.query}%"),
    )
    rows = await cursor.fetchall()

    from ..core.language import detect_language

    results = []
    for row in rows[:body.limit]:
        fp = row["file_path"]
        results.append(FileResultItem(file_path=fp, language=detect_language(fp)))

    return FileSearchResponse(results=results, total=len(results))


@router.post("/{project_path}/search/definitions", response_model=DefinitionResponse)
async def definition_search(project_path: str, body: DefinitionRequest):
    """Go to Definition — find where a symbol is defined."""
    project_path = await resolve_project_path(project_path)
    await _get_project(project_path)

    db = await get_db()

    # Exact name match in symbols table
    sql = "SELECT * FROM symbols WHERE project_path = ? AND name = ?"
    params: list = [project_path, body.symbol]

    if body.kind:
        sql += " AND kind = ?"
        params.append(body.kind)

    if body.file_path:
        sql += " AND file_path = ?"
        params.append(body.file_path)

    sql += " ORDER BY name LIMIT ?"
    params.append(body.limit)

    cursor = await db.execute(sql, params)
    rows = await cursor.fetchall()

    # If no exact match, try case-insensitive
    if not rows:
        sql = "SELECT * FROM symbols WHERE project_path = ? AND name LIKE ?"
        params = [project_path, body.symbol]

        if body.kind:
            sql += " AND kind = ?"
            params.append(body.kind)

        if body.file_path:
            sql += " AND file_path = ?"
            params.append(body.file_path)

        sql += " ORDER BY name LIMIT ?"
        params.append(body.limit)

        cursor = await db.execute(sql, params)
        rows = await cursor.fetchall()

    results = [
        DefinitionItem(
            name=row["name"],
            kind=row["kind"],
            file_path=row["file_path"],
            line=row["line"],
            end_line=row["end_line"],
            language=row["language"],
            signature=row["signature"],
            parent_name=row["parent_name"],
        )
        for row in rows
    ]

    return DefinitionResponse(results=results, total=len(results))


@router.post("/{project_path}/search/references", response_model=ReferenceResponse)
async def reference_search(project_path: str, body: ReferenceRequest):
    """Find References — find all code chunks where a symbol is used."""
    project_path = await resolve_project_path(project_path)
    await _get_project(project_path)

    collection = vector_store_service.get_or_create_collection(project_path)

    # Search ChromaDB for chunks containing the symbol name
    where_doc = {"$contains": body.symbol}
    where_filter = None
    if body.file_path:
        where_filter = {"file_path": body.file_path}

    try:
        query_result = collection.get(
            where_document=where_doc,
            where=where_filter,
            include=["documents", "metadatas"],
            limit=body.limit,
        )
    except Exception:
        # Fallback: no results
        return ReferenceResponse(results=[], total=0)

    results = []
    if query_result and query_result["ids"]:
        for i in range(len(query_result["ids"])):
            metadata = query_result["metadatas"][i]
            content = query_result["documents"][i]

            results.append(ReferenceItem(
                file_path=metadata["file_path"],
                start_line=metadata["start_line"],
                end_line=metadata["end_line"],
                content=content,
                chunk_type=metadata["chunk_type"],
                symbol_name=metadata.get("symbol_name", ""),
                language=metadata.get("language", ""),
            ))

    # Sort: definitions first (where symbol_name matches), then by file_path
    results.sort(key=lambda r: (r.symbol_name != body.symbol, r.file_path, r.start_line))

    return ReferenceResponse(results=results, total=len(results))


@router.get("/{project_path}/summary", response_model=ProjectSummary)
async def project_summary(project_path: str):
    project_path = await resolve_project_path(project_path)
    project = await _get_project(project_path)
    stats = json.loads(project["stats"])
    languages = json.loads(project["languages"])

    # Top directories
    db = await get_db()
    cursor = await db.execute(
        "SELECT file_path FROM file_hashes WHERE project_path = ?",
        (project_path,),
    )
    rows = await cursor.fetchall()

    dir_counter: Counter = Counter()
    for row in rows:
        parts = Path(row["file_path"]).parts
        if len(parts) > 3:
            dir_counter[str(Path(*parts[:4]))] += 1
        elif len(parts) > 1:
            dir_counter[str(Path(*parts[:2]))] += 1

    top_dirs = [
        {"path": path, "file_count": count}
        for path, count in dir_counter.most_common(10)
    ]

    # Recent symbols + accurate count directly from DB
    cursor = await db.execute(
        "SELECT name, kind, file_path, language FROM symbols WHERE project_path = ? LIMIT 20",
        (project_path,),
    )
    symbol_rows = await cursor.fetchall()
    recent_symbols = [
        {"name": r["name"], "kind": r["kind"], "file_path": r["file_path"], "language": r["language"]}
        for r in symbol_rows
    ]

    cursor = await db.execute(
        "SELECT COUNT(*) as cnt FROM symbols WHERE project_path = ?",
        (project_path,),
    )
    row = await cursor.fetchone()
    total_symbols = row["cnt"] if row else 0

    return ProjectSummary(
        host_path=project_path,
        status=project["status"],
        languages=languages,
        total_files=stats.get("total_files", 0),
        total_chunks=stats.get("total_chunks", 0),
        total_symbols=total_symbols,
        top_directories=top_dirs,
        recent_symbols=recent_symbols,
    )
