import asyncio
import gc
import json
import logging
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone

from ..config import settings
from ..database import get_db
from .chunker import chunker_service
from .embeddings import embedding_service
from .file_discovery import file_discovery_service
from .reference_index import reference_index_service
from .symbol_index import SymbolInfo, symbol_index_service
from .vector_store import vector_store_service

logger = logging.getLogger(__name__)


@dataclass
class IndexProgress:
    run_id: str
    project_path: str
    status: str = "queued"  # queued|indexing|completed|failed|cancelled
    phase: str = "queued"   # queued|discovering|chunking|embedding|storing|completed
    files_discovered: int = 0
    files_processed: int = 0
    files_total: int = 0
    chunks_created: int = 0
    elapsed_seconds: float = 0
    estimated_remaining: float = 0
    error_message: str | None = None


@dataclass
class SessionState:
    run_id: str
    project_path: str
    files_processed: int = 0
    chunks_created: int = 0
    languages_seen: set = field(default_factory=set)
    start_time: float = field(default_factory=time.time)
    status: str = "active"


class IndexerService:
    def __init__(self):
        self._active_jobs: dict[str, IndexProgress] = {}
        self._cancel_events: dict[str, asyncio.Event] = {}
        self._active_sessions: dict[str, SessionState] = {}  # run_id -> SessionState

    # ---- New three-phase protocol ----

    async def begin_indexing(self, project_path: str, full: bool = False) -> tuple[str, dict[str, str]]:
        """Phase 1: Create indexing session, return stored hashes."""
        run_id = str(uuid.uuid4())
        db = await get_db()
        now = datetime.now(timezone.utc).isoformat()

        await db.execute(
            "INSERT INTO index_runs (id, project_path, started_at, status) VALUES (?, ?, ?, ?)",
            (run_id, project_path, now, "running"),
        )
        await db.execute(
            "UPDATE projects SET status = 'indexing', updated_at = ? WHERE host_path = ?",
            (now, project_path),
        )
        await db.commit()

        stored_hashes: dict[str, str] = {}

        if full:
            vector_store_service.delete_collection(project_path)
            await db.execute("DELETE FROM file_hashes WHERE project_path = ?", (project_path,))
            await db.execute("DELETE FROM symbols WHERE project_path = ?", (project_path,))
            await db.execute("DELETE FROM refs WHERE project_path = ?", (project_path,))
            await db.commit()
        else:
            cursor = await db.execute(
                "SELECT file_path, content_hash FROM file_hashes WHERE project_path = ?",
                (project_path,),
            )
            rows = await cursor.fetchall()
            stored_hashes = {row["file_path"]: row["content_hash"] for row in rows}

        session = SessionState(run_id=run_id, project_path=project_path)
        self._active_sessions[run_id] = session

        progress = IndexProgress(run_id=run_id, project_path=project_path, status="indexing", phase="receiving")
        self._active_jobs[project_path] = progress

        asyncio.create_task(self._session_ttl_cleanup(run_id))

        return run_id, stored_hashes

    async def process_files(self, project_path: str, run_id: str, files: list) -> tuple[int, int, int]:
        """Phase 2: Process a batch of files (chunk, embed, store). Synchronous within request."""
        session = self._active_sessions.get(run_id)
        if not session:
            raise ValueError(f"No active session for run_id {run_id}")
        if session.project_path != project_path:
            raise ValueError("run_id does not match project")

        logger.info("Processing batch of %d files for session %s", len(files), run_id)

        db = await get_db()
        now = datetime.now(timezone.utc).isoformat()
        files_accepted = 0
        batch_chunks = 0
        batch_symbols: list[SymbolInfo] = []
        batch_references = []

        for file_payload in files:
            try:
                content = file_payload.content
                if not content.strip():
                    continue

                language = file_payload.language or "text"
                session.languages_seen.add(language)

                result = chunker_service.chunk_file(file_payload.path, content, language)
                chunks = result.chunks
                if not chunks:
                    continue

                for chunk in chunks:
                    if chunk.symbol_name and chunk.chunk_type in (
                        "function", "class", "method", "type"
                    ):
                        batch_symbols.append(
                            SymbolInfo(
                                name=chunk.symbol_name,
                                kind=chunk.chunk_type,
                                file_path=chunk.file_path,
                                line=chunk.start_line,
                                end_line=chunk.end_line,
                                language=chunk.language,
                                signature=chunk.symbol_signature,
                                parent_name=chunk.parent_name,
                            )
                        )

                batch_references.extend(result.references)

                texts = [f"{c.chunk_type}: {c.content}" for c in chunks]
                embeddings = await embedding_service.embed_texts(texts)

                # Delete old chunks, symbols, and references BEFORE inserting new ones
                await vector_store_service.delete_by_file(project_path, file_payload.path)
                await symbol_index_service.delete_by_file(project_path, file_payload.path)
                await reference_index_service.delete_by_file(project_path, file_payload.path)

                await vector_store_service.upsert_chunks(project_path, chunks, embeddings)
                batch_chunks += len(chunks)

                await db.execute(
                    """INSERT OR REPLACE INTO file_hashes
                       (project_path, file_path, content_hash, indexed_at)
                       VALUES (?, ?, ?, ?)""",
                    (project_path, file_payload.path, file_payload.content_hash, now),
                )
                files_accepted += 1

            except Exception as e:
                logger.error("Error processing %s: %s", file_payload.path, e)
                continue

        if batch_symbols:
            await symbol_index_service.upsert_symbols(project_path, batch_symbols)
        if batch_references:
            await reference_index_service.upsert_references(project_path, batch_references)
        await db.commit()
        gc.collect()

        session.files_processed += files_accepted
        session.chunks_created += batch_chunks

        progress = self._active_jobs.get(project_path)
        if progress:
            progress.files_processed = session.files_processed
            progress.chunks_created = session.chunks_created
            progress.elapsed_seconds = time.time() - session.start_time

        logger.info(
            "Batch done: %d files accepted, %d chunks. Total: %d files, %d chunks",
            files_accepted, batch_chunks, session.files_processed, session.chunks_created,
        )

        return files_accepted, batch_chunks, session.files_processed

    async def finish_indexing(
        self, project_path: str, run_id: str,
        deleted_paths: list[str], total_files_discovered: int,
    ) -> tuple[str, int, int]:
        """Phase 3: Clean up deleted files, update project stats, close session."""
        session = self._active_sessions.get(run_id)
        if not session:
            raise ValueError(f"No active session for run_id {run_id}")
        if session.project_path != project_path:
            raise ValueError("run_id does not match project")

        db = await get_db()
        now = datetime.now(timezone.utc).isoformat()

        for del_path in deleted_paths:
            await vector_store_service.delete_by_file(project_path, del_path)
            await symbol_index_service.delete_by_file(project_path, del_path)
            await reference_index_service.delete_by_file(project_path, del_path)
            await db.execute(
                "DELETE FROM file_hashes WHERE project_path = ? AND file_path = ?",
                (project_path, del_path),
            )

        # Compute accurate stats from DB (not just this session)
        cursor = await db.execute(
            "SELECT COUNT(*) as cnt FROM file_hashes WHERE project_path = ?",
            (project_path,),
        )
        row = await cursor.fetchone()
        total_indexed_files = row["cnt"] if row else session.files_processed

        cursor = await db.execute(
            "SELECT COUNT(*) as cnt FROM symbols WHERE project_path = ?",
            (project_path,),
        )
        row = await cursor.fetchone()
        total_symbols = row["cnt"] if row else 0

        # Get total chunks from vector store collection
        try:
            collection = vector_store_service.get_or_create_collection(project_path)
            total_chunks = collection.count()
        except Exception:
            total_chunks = session.chunks_created

        # Collect all languages from indexed files
        from ..core.language import detect_language
        cursor = await db.execute(
            "SELECT file_path FROM file_hashes WHERE project_path = ?",
            (project_path,),
        )
        all_files = await cursor.fetchall()
        all_languages: set[str] = set()
        for f in all_files:
            lang = detect_language(f["file_path"])
            if lang:
                all_languages.add(lang)

        stats = {
            "total_files": total_files_discovered,
            "indexed_files": total_indexed_files,
            "total_chunks": total_chunks,
            "total_symbols": total_symbols,
        }
        await db.execute(
            """UPDATE projects
               SET stats = ?, languages = ?, status = 'indexed',
                   last_indexed_at = ?, updated_at = ?
               WHERE host_path = ?""",
            (
                json.dumps(stats),
                json.dumps(sorted(all_languages)),
                now, now, project_path,
            ),
        )

        await db.execute(
            """UPDATE index_runs
               SET status = 'completed', completed_at = ?,
                   files_processed = ?, chunks_created = ?
               WHERE id = ?""",
            (now, session.files_processed, session.chunks_created, run_id),
        )
        await db.commit()

        progress = self._active_jobs.get(project_path)
        if progress:
            progress.status = "completed"
            progress.phase = "completed"

        session.status = "completed"

        async def _cleanup():
            await asyncio.sleep(60)
            self._active_sessions.pop(run_id, None)
            self._active_jobs.pop(project_path, None)

        asyncio.create_task(_cleanup())

        return "completed", session.files_processed, session.chunks_created

    async def _session_ttl_cleanup(self, run_id: str):
        """Remove stale sessions after 1 hour."""
        await asyncio.sleep(3600)
        session = self._active_sessions.pop(run_id, None)
        if session and session.status == "active":
            logger.warning("Session %s timed out, cleaning up", run_id)
            self._active_jobs.pop(session.project_path, None)

    # ---- Legacy methods ----

    async def start_indexing(self, project_path: str, full: bool = False, batch_size: int = 20) -> str:
        if project_path in self._active_jobs:
            existing = self._active_jobs[project_path]
            if existing.status in ("queued", "indexing"):
                return existing.run_id

        run_id = str(uuid.uuid4())
        progress = IndexProgress(run_id=run_id, project_path=project_path)
        self._active_jobs[project_path] = progress

        cancel_event = asyncio.Event()
        self._cancel_events[project_path] = cancel_event

        # Record run in DB
        db = await get_db()
        now = datetime.now(timezone.utc).isoformat()
        await db.execute(
            "INSERT INTO index_runs (id, project_path, started_at, status) VALUES (?, ?, ?, ?)",
            (run_id, project_path, now, "running"),
        )
        await db.execute(
            "UPDATE projects SET status = 'indexing', updated_at = ? WHERE host_path = ?",
            (now, project_path),
        )
        await db.commit()

        asyncio.create_task(self._run_pipeline(project_path, run_id, cancel_event, full, batch_size))
        return run_id

    async def get_progress(self, project_path: str) -> IndexProgress | None:
        return self._active_jobs.get(project_path)

    async def cancel(self, project_path: str) -> bool:
        event = self._cancel_events.get(project_path)
        if event:
            event.set()
            return True
        return False

    async def _run_pipeline(
        self, project_path: str, run_id: str,
        cancel_event: asyncio.Event, full: bool = False,
        batch_size: int = 20,
    ):
        progress = self._active_jobs[project_path]
        progress.status = "indexing"
        start_time = time.time()

        try:
            db = await get_db()

            # Get project info
            cursor = await db.execute(
                "SELECT * FROM projects WHERE host_path = ?", (project_path,)
            )
            project = await cursor.fetchone()
            if not project:
                raise ValueError(f"Project {project_path} not found")

            container_path = project["container_path"]
            proj_settings = json.loads(project["settings"])
            exclude_patterns = proj_settings.get(
                "exclude_patterns", settings.excluded_dirs_list
            )
            max_file_size = proj_settings.get("max_file_size", settings.max_file_size)

            # Phase 1: Discover files
            progress.phase = "discovering"
            discovered = await asyncio.get_event_loop().run_in_executor(
                None,
                lambda: file_discovery_service.discover(
                    container_path, exclude_patterns, max_file_size
                ),
            )
            progress.files_discovered = len(discovered)

            if cancel_event.is_set():
                await self._finish_run(project_path, run_id, "cancelled", progress)
                return

            # Get stored hashes for incremental
            if not full:
                cursor = await db.execute(
                    "SELECT file_path, content_hash FROM file_hashes WHERE project_path = ?",
                    (project_path,),
                )
                rows = await cursor.fetchall()
                stored_hashes = {row["file_path"]: row["content_hash"] for row in rows}
                to_process, deleted = file_discovery_service.get_changed_files(
                    discovered, stored_hashes
                )

                # Remove deleted files
                for del_path in deleted:
                    await vector_store_service.delete_by_file(project_path, del_path)
                    await symbol_index_service.delete_by_file(project_path, del_path)
                    await reference_index_service.delete_by_file(project_path, del_path)
                    await db.execute(
                        "DELETE FROM file_hashes WHERE project_path = ? AND file_path = ?",
                        (project_path, del_path),
                    )
            else:
                to_process = discovered
                # Clear all existing data for full reindex
                vector_store_service.delete_collection(project_path)
                await db.execute(
                    "DELETE FROM file_hashes WHERE project_path = ?", (project_path,)
                )
                await db.execute(
                    "DELETE FROM symbols WHERE project_path = ?", (project_path,)
                )
                await db.execute(
                    "DELETE FROM refs WHERE project_path = ?", (project_path,)
                )

            progress.files_total = len(to_process)
            files_discovered_count = len(discovered)
            # Free discovery data — no longer needed
            del discovered
            gc.collect()

            await db.execute(
                "UPDATE index_runs SET files_total = ? WHERE id = ?",
                (len(to_process), run_id),
            )
            await db.commit()

            if not to_process:
                await self._finish_run(project_path, run_id, "completed", progress)
                return

            # Phase 2-4: Process files in batches to limit memory usage
            BATCH_COMMIT_SIZE = max(1, batch_size)  # commit DB and flush symbols every N files
            batch_symbols: list[SymbolInfo] = []
            batch_references = []
            total_chunks = 0
            total_symbols = 0
            now = datetime.now(timezone.utc).isoformat()
            languages_seen: set[str] = set()

            for i, file_info in enumerate(to_process):
                if cancel_event.is_set():
                    await self._finish_run(project_path, run_id, "cancelled", progress)
                    return

                progress.phase = "chunking"
                progress.files_processed = i
                progress.elapsed_seconds = time.time() - start_time
                if i > 0:
                    progress.estimated_remaining = (
                        progress.elapsed_seconds / i * (len(to_process) - i)
                    )

                try:
                    # Read file
                    with open(file_info.path, "r", errors="ignore") as f:
                        content = f.read()

                    if not content.strip():
                        continue

                    language = file_info.language or "text"
                    languages_seen.add(language)

                    # Chunk
                    result = chunker_service.chunk_file(
                        file_info.host_path, content, language
                    )
                    chunks = result.chunks
                    if not chunks:
                        continue

                    # Collect symbols from this file
                    for chunk in chunks:
                        if chunk.symbol_name and chunk.chunk_type in (
                            "function", "class", "method", "type"
                        ):
                            batch_symbols.append(
                                SymbolInfo(
                                    name=chunk.symbol_name,
                                    kind=chunk.chunk_type,
                                    file_path=chunk.file_path,
                                    line=chunk.start_line,
                                    end_line=chunk.end_line,
                                    language=chunk.language,
                                    signature=chunk.symbol_signature,
                                    parent_name=chunk.parent_name,
                                )
                            )

                    batch_references.extend(result.references)

                    # Embed
                    progress.phase = "embedding"
                    texts = [
                        f"{c.chunk_type}: {c.content}" for c in chunks
                    ]
                    embeddings = await embedding_service.embed_texts(texts)

                    # Delete old data BEFORE inserting new
                    progress.phase = "storing"
                    await vector_store_service.delete_by_file(project_path, file_info.host_path)
                    await symbol_index_service.delete_by_file(project_path, file_info.host_path)
                    await reference_index_service.delete_by_file(project_path, file_info.host_path)

                    # Store in vector DB
                    await vector_store_service.upsert_chunks(
                        project_path, chunks, embeddings
                    )

                    total_chunks += len(chunks)
                    progress.chunks_created = total_chunks

                    # Update file hash
                    await db.execute(
                        """INSERT OR REPLACE INTO file_hashes
                           (project_path, file_path, content_hash, indexed_at)
                           VALUES (?, ?, ?, ?)""",
                        (project_path, file_info.host_path, file_info.content_hash, now),
                    )

                except Exception as e:
                    logger.error("Error processing %s: %s", file_info.path, e)
                    continue

                # Flush batch: commit DB, store symbols/refs, free memory every N files
                if (i + 1) % BATCH_COMMIT_SIZE == 0:
                    if batch_symbols:
                        await symbol_index_service.upsert_symbols(project_path, batch_symbols)
                        total_symbols += len(batch_symbols)
                        batch_symbols = []
                    if batch_references:
                        await reference_index_service.upsert_references(project_path, batch_references)
                        batch_references = []
                    await db.commit()
                    gc.collect()
                    logger.debug("Batch committed: %d/%d files", i + 1, len(to_process))

            # Flush remaining symbols and references
            if batch_symbols:
                await symbol_index_service.upsert_symbols(project_path, batch_symbols)
                total_symbols += len(batch_symbols)
            if batch_references:
                await reference_index_service.upsert_references(project_path, batch_references)

            # Update project stats
            progress.files_processed = len(to_process)
            stats = {
                "total_files": files_discovered_count,
                "indexed_files": progress.files_processed,
                "total_chunks": total_chunks,
                "total_symbols": total_symbols,
            }
            await db.execute(
                """UPDATE projects
                   SET stats = ?, languages = ?, status = 'indexed',
                       last_indexed_at = ?, updated_at = ?
                   WHERE host_path = ?""",
                (
                    json.dumps(stats),
                    json.dumps(sorted(languages_seen)),
                    now,
                    now,
                    project_path,
                ),
            )
            await db.commit()

            await self._finish_run(project_path, run_id, "completed", progress)

        except Exception as e:
            logger.exception("Indexing failed for project %s", project_path)
            progress.status = "failed"
            progress.error_message = str(e)
            await self._finish_run(project_path, run_id, "failed", progress, str(e))

    async def _finish_run(
        self, project_path: str, run_id: str, status: str,
        progress: IndexProgress, error: str | None = None,
    ):
        progress.status = status
        progress.phase = "completed" if status == "completed" else status
        now = datetime.now(timezone.utc).isoformat()

        db = await get_db()
        await db.execute(
            """UPDATE index_runs
               SET status = ?, completed_at = ?, files_processed = ?,
                   chunks_created = ?, error_message = ?
               WHERE id = ?""",
            (status, now, progress.files_processed, progress.chunks_created, error, run_id),
        )

        if status != "completed":
            await db.execute(
                "UPDATE projects SET status = ?, updated_at = ? WHERE host_path = ?",
                ("error" if status == "failed" else status, now, project_path),
            )
        await db.commit()

        # Clean up after a delay
        async def _cleanup():
            await asyncio.sleep(60)
            self._active_jobs.pop(project_path, None)
            self._cancel_events.pop(project_path, None)

        asyncio.create_task(_cleanup())


indexer_service = IndexerService()
