"""Project path hashing for safe URL routing.

Project paths contain slashes which conflict with FastAPI path routing.
We use SHA1 hash as a short, URL-safe identifier for projects.
The actual path is stored in the database and resolved by hash.
"""

import hashlib


def hash_project_path(path: str) -> str:
    """Compute SHA1 hash of a project path (first 16 hex chars)."""
    return hashlib.sha1(path.encode()).hexdigest()[:16]


async def resolve_project_path(path_hash: str) -> str:
    """Look up the actual project path by its SHA1 hash prefix."""
    from ..core.exceptions import ProjectNotFoundError
    from ..database import get_db

    db = await get_db()
    cursor = await db.execute("SELECT host_path FROM projects")
    rows = await cursor.fetchall()

    for row in rows:
        if hash_project_path(row["host_path"]) == path_hash:
            return row["host_path"]

    raise ProjectNotFoundError(path_hash)
