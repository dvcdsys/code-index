from dataclasses import dataclass

from ..database import get_db
from .chunker import ReferenceInfo


class ReferenceIndexService:
    async def upsert_references(self, project_path: str, refs: list[ReferenceInfo]):
        if not refs:
            return
        db = await get_db()
        await db.executemany(
            """INSERT INTO refs (project_path, name, file_path, line, col, language)
               VALUES (?, ?, ?, ?, ?, ?)""",
            [
                (project_path, r.name, r.file_path, r.line, r.col, r.language)
                for r in refs
            ],
        )
        await db.commit()

    async def delete_by_file(self, project_path: str, file_path: str):
        db = await get_db()
        await db.execute(
            "DELETE FROM refs WHERE project_path = ? AND file_path = ?",
            (project_path, file_path),
        )
        await db.commit()

    async def delete_by_project(self, project_path: str):
        db = await get_db()
        await db.execute(
            "DELETE FROM refs WHERE project_path = ?",
            (project_path,),
        )
        await db.commit()

    async def search(
        self,
        project_path: str,
        name: str,
        file_path: str | None = None,
        limit: int = 50,
    ) -> list[ReferenceInfo]:
        db = await get_db()
        sql = "SELECT name, file_path, line, col, language FROM refs WHERE project_path = ? AND name = ?"
        params: list = [project_path, name]

        if file_path:
            sql += " AND file_path = ?"
            params.append(file_path)

        sql += " ORDER BY file_path, line LIMIT ?"
        params.append(limit)

        cursor = await db.execute(sql, params)
        rows = await cursor.fetchall()
        return [
            ReferenceInfo(
                name=row["name"],
                file_path=row["file_path"],
                line=row["line"],
                col=row["col"],
                language=row["language"],
            )
            for row in rows
        ]


reference_index_service = ReferenceIndexService()