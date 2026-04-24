import uuid
from dataclasses import dataclass

from ..database import get_db


@dataclass
class SymbolInfo:
    name: str
    kind: str               # function|class|method|type
    file_path: str          # host path
    line: int
    end_line: int
    language: str
    signature: str | None = None
    parent_name: str | None = None
    docstring: str | None = None


class SymbolIndexService:
    async def upsert_symbols(self, project_path: str, symbols: list[SymbolInfo]):
        db = await get_db()
        for symbol in symbols:
            symbol_id = str(uuid.uuid4())
            await db.execute(
                """INSERT OR REPLACE INTO symbols
                   (id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
                (
                    symbol_id,
                    project_path,
                    symbol.name,
                    symbol.kind,
                    symbol.file_path,
                    symbol.line,
                    symbol.end_line,
                    symbol.language,
                    symbol.signature,
                    symbol.parent_name,
                    symbol.docstring,
                ),
            )
        await db.commit()

    async def search(
        self,
        project_path: str,
        query: str,
        kinds: list[str] | None = None,
        limit: int = 20,
    ) -> list[SymbolInfo]:
        db = await get_db()

        # Try exact match first, then prefix, then contains
        for pattern in [query, f"{query}%", f"%{query}%"]:
            sql = "SELECT * FROM symbols WHERE project_path = ? AND name LIKE ?"
            params: list = [project_path, pattern]

            if kinds:
                placeholders = ",".join("?" for _ in kinds)
                sql += f" AND kind IN ({placeholders})"
                params.extend(kinds)

            sql += f" ORDER BY name LIMIT ?"
            params.append(limit)

            cursor = await db.execute(sql, params)
            rows = await cursor.fetchall()

            if rows:
                return [
                    SymbolInfo(
                        name=row["name"],
                        kind=row["kind"],
                        file_path=row["file_path"],
                        line=row["line"],
                        end_line=row["end_line"],
                        language=row["language"],
                        signature=row["signature"],
                        parent_name=row["parent_name"],
                        docstring=row["docstring"],
                    )
                    for row in rows
                ]

        return []

    async def delete_by_file(self, project_path: str, file_path: str):
        db = await get_db()
        await db.execute(
            "DELETE FROM symbols WHERE project_path = ? AND file_path = ?",
            (project_path, file_path),
        )
        await db.commit()

    async def get_project_symbols(self, project_path: str) -> list[SymbolInfo]:
        db = await get_db()
        cursor = await db.execute(
            "SELECT * FROM symbols WHERE project_path = ? ORDER BY kind, name",
            (project_path,),
        )
        rows = await cursor.fetchall()
        return [
            SymbolInfo(
                name=row["name"],
                kind=row["kind"],
                file_path=row["file_path"],
                line=row["line"],
                end_line=row["end_line"],
                language=row["language"],
                signature=row["signature"],
                parent_name=row["parent_name"],
                docstring=row["docstring"],
            )
            for row in rows
        ]


symbol_index_service = SymbolIndexService()
