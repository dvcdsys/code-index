from pydantic import BaseModel, Field


class IndexRequest(BaseModel):
    """DEPRECATED: Use the three-phase protocol instead."""
    full: bool = False
    batch_size: int = 20  # files per batch (lower = less memory, slower)


class IndexProgressResponse(BaseModel):
    status: str          # idle|queued|indexing|completed|failed|cancelled
    progress: dict | None = None


class IndexTriggerResponse(BaseModel):
    run_id: str
    message: str


# --- New three-phase protocol ---

class IndexBeginRequest(BaseModel):
    full: bool = False


class IndexBeginResponse(BaseModel):
    run_id: str
    stored_hashes: dict[str, str]  # {file_path: sha256_hash}


class FilePayload(BaseModel):
    path: str
    content: str
    content_hash: str  # SHA-256
    language: str | None = None
    size: int = 0


class IndexFilesRequest(BaseModel):
    run_id: str
    files: list[FilePayload] = Field(..., max_length=50)


class IndexFilesResponse(BaseModel):
    files_accepted: int
    chunks_created: int
    files_processed_total: int


class IndexFinishRequest(BaseModel):
    run_id: str
    deleted_paths: list[str] = []
    total_files_discovered: int = 0


class IndexFinishResponse(BaseModel):
    status: str
    files_processed: int
    chunks_created: int
