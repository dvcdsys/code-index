from fastapi import APIRouter, Depends

from ..auth import verify_api_key
from ..database import get_db

router = APIRouter()


@router.get("/health")
async def health():
    return {"status": "ok"}


@router.get("/api/v1/status", dependencies=[Depends(verify_api_key)])
async def status():
    db = await get_db()
    cursor = await db.execute("SELECT COUNT(*) FROM projects")
    row = await cursor.fetchone()
    project_count = row[0] if row else 0

    cursor = await db.execute(
        "SELECT COUNT(*) FROM index_runs WHERE status = 'running'"
    )
    row = await cursor.fetchone()
    active_jobs = row[0] if row else 0

    return {
        "status": "ok",
        "model_loaded": True,
        "projects": project_count,
        "active_indexing_jobs": active_jobs,
    }
