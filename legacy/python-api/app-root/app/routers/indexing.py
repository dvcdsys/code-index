from ..core.path_encoding import resolve_project_path

from fastapi import APIRouter, Depends, HTTPException, status

from ..auth import verify_api_key
from ..core.exceptions import ProjectNotFoundError
from ..database import get_db
from ..schemas.indexing import (
    IndexBeginRequest,
    IndexBeginResponse,
    IndexFilesRequest,
    IndexFilesResponse,
    IndexFinishRequest,
    IndexFinishResponse,
    IndexProgressResponse,
    IndexRequest,
    IndexTriggerResponse,
)
from ..services.embeddings import EmbeddingBusyError
from ..services.indexer import indexer_service

router = APIRouter(
    prefix="/api/v1/projects",
    dependencies=[Depends(verify_api_key)],
)


async def _ensure_project(project_path: str):
    db = await get_db()
    cursor = await db.execute("SELECT host_path FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    if not row:
        raise ProjectNotFoundError(project_path)


@router.post(
    "/{project_path}/index",
    status_code=status.HTTP_202_ACCEPTED,
    response_model=IndexTriggerResponse,
)
async def trigger_index(project_path: str, body: IndexRequest | None = None):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    full = body.full if body else False
    batch_size = body.batch_size if body else 20
    run_id = await indexer_service.start_indexing(project_path, full=full, batch_size=batch_size)
    return IndexTriggerResponse(
        run_id=run_id,
        message="Indexing started" if not full else "Full reindex started",
    )


@router.get("/{project_path}/index/status", response_model=IndexProgressResponse)
async def index_status(project_path: str):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    progress = await indexer_service.get_progress(project_path)

    if progress is None:
        # Check last run
        db = await get_db()
        cursor = await db.execute(
            "SELECT * FROM index_runs WHERE project_path = ? ORDER BY started_at DESC LIMIT 1",
            (project_path,),
        )
        row = await cursor.fetchone()
        if row:
            return IndexProgressResponse(
                status=row["status"],
                progress={
                    "files_processed": row["files_processed"],
                    "files_total": row["files_total"],
                    "chunks_created": row["chunks_created"],
                },
            )
        return IndexProgressResponse(status="idle")

    return IndexProgressResponse(
        status=progress.status,
        progress={
            "phase": progress.phase,
            "files_discovered": progress.files_discovered,
            "files_processed": progress.files_processed,
            "files_total": progress.files_total,
            "chunks_created": progress.chunks_created,
            "elapsed_seconds": round(progress.elapsed_seconds, 1),
            "estimated_remaining": round(progress.estimated_remaining, 1),
        },
    )


@router.post("/{project_path}/index/cancel")
async def cancel_index(project_path: str):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    cancelled = await indexer_service.cancel(project_path)
    if not cancelled:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail="No active indexing job found",
        )
    return {"message": "Indexing cancellation requested"}


# --- New three-phase protocol endpoints ---

@router.post(
    "/{project_path}/index/begin",
    response_model=IndexBeginResponse,
)
async def begin_index(project_path: str, body: IndexBeginRequest | None = None):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    full = body.full if body else False
    run_id, stored_hashes = await indexer_service.begin_indexing(project_path, full=full)
    return IndexBeginResponse(run_id=run_id, stored_hashes=stored_hashes)


@router.post(
    "/{project_path}/index/files",
    response_model=IndexFilesResponse,
)
async def index_files(project_path: str, body: IndexFilesRequest):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    try:
        files_accepted, chunks_created, total = await indexer_service.process_files(
            project_path, body.run_id, body.files,
        )
    except EmbeddingBusyError as exc:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail=f"GPU is busy processing another embedding request, retry after {exc.retry_after}s",
            headers={"Retry-After": str(exc.retry_after)},
        )
    return IndexFilesResponse(
        files_accepted=files_accepted,
        chunks_created=chunks_created,
        files_processed_total=total,
    )


@router.post(
    "/{project_path}/index/finish",
    response_model=IndexFinishResponse,
)
async def finish_index(project_path: str, body: IndexFinishRequest):
    project_path = await resolve_project_path(project_path)
    await _ensure_project(project_path)
    status_str, files_processed, chunks_created = await indexer_service.finish_indexing(
        project_path, body.run_id, body.deleted_paths, body.total_files_discovered,
    )
    return IndexFinishResponse(
        status=status_str,
        files_processed=files_processed,
        chunks_created=chunks_created,
    )
