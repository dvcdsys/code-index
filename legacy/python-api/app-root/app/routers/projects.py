import json
from datetime import datetime, timezone
from ..core.path_encoding import resolve_project_path

from fastapi import APIRouter, Depends, HTTPException, status

from ..auth import verify_api_key
from ..core.exceptions import ProjectNotFoundError
from ..database import get_db
from ..schemas.project import (
    ProjectCreate,
    ProjectListResponse,
    ProjectResponse,
    ProjectSettings,
    ProjectStats,
    ProjectUpdate,
)

router = APIRouter(
    prefix="/api/v1/projects",
    dependencies=[Depends(verify_api_key)],
)


def _row_to_project(row) -> ProjectResponse:
    return ProjectResponse(
        host_path=row["host_path"],
        container_path=row["container_path"],
        languages=json.loads(row["languages"]),
        settings=ProjectSettings(**json.loads(row["settings"])),
        stats=ProjectStats(**json.loads(row["stats"])),
        status=row["status"],
        created_at=row["created_at"],
        updated_at=row["updated_at"],
        last_indexed_at=row["last_indexed_at"],
    )


@router.post("", status_code=status.HTTP_201_CREATED, response_model=ProjectResponse)
async def create_project(body: ProjectCreate):
    db = await get_db()
    now = datetime.now(timezone.utc).isoformat()
    container_path = body.host_path
    default_settings = ProjectSettings()
    default_stats = ProjectStats()

    try:
        await db.execute(
            """INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                body.host_path,
                container_path,
                "[]",
                default_settings.model_dump_json(),
                default_stats.model_dump_json(),
                "created",
                now,
                now,
            ),
        )
        await db.commit()
    except Exception as e:
        if "UNIQUE" in str(e):
            raise HTTPException(
                status_code=status.HTTP_409_CONFLICT,
                detail=f"Project at path '{body.host_path}' already exists",
            )
        raise

    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (body.host_path,))
    row = await cursor.fetchone()
    return _row_to_project(row)


@router.get("", response_model=ProjectListResponse)
async def list_projects():
    db = await get_db()
    cursor = await db.execute("SELECT * FROM projects ORDER BY created_at DESC")
    rows = await cursor.fetchall()
    projects = [_row_to_project(row) for row in rows]
    return ProjectListResponse(projects=projects, total=len(projects))


@router.get("/{project_path}", response_model=ProjectResponse)
async def get_project(project_path: str):
    project_path = await resolve_project_path(project_path)
    db = await get_db()
    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    if not row:
        raise ProjectNotFoundError(project_path)
    return _row_to_project(row)


@router.patch("/{project_path}", response_model=ProjectResponse)
async def update_project(project_path: str, body: ProjectUpdate):
    project_path = await resolve_project_path(project_path)
    db = await get_db()
    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    if not row:
        raise ProjectNotFoundError(project_path)

    now = datetime.now(timezone.utc).isoformat()
    updates = []
    values = []

    if body.settings is not None:
        updates.append("settings = ?")
        values.append(body.settings.model_dump_json())

    if updates:
        updates.append("updated_at = ?")
        values.append(now)
        values.append(project_path)
        await db.execute(
            f"UPDATE projects SET {', '.join(updates)} WHERE host_path = ?", values
        )
        await db.commit()

    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    return _row_to_project(row)


@router.delete("/{project_path}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_project(project_path: str):
    project_path = await resolve_project_path(project_path)
    db = await get_db()
    cursor = await db.execute("SELECT * FROM projects WHERE host_path = ?", (project_path,))
    row = await cursor.fetchone()
    if not row:
        raise ProjectNotFoundError(project_path)

    # Delete ChromaDB collection
    from ..services.vector_store import vector_store_service
    vector_store_service.delete_collection(project_path)

    await db.execute("DELETE FROM projects WHERE host_path = ?", (project_path,))
    await db.commit()
