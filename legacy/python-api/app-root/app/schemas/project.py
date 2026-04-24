from datetime import datetime

from pydantic import BaseModel, Field


class ProjectSettings(BaseModel):
    exclude_patterns: list[str] = Field(
        default_factory=lambda: ["node_modules", ".git", ".venv", "__pycache__", "dist", "build", ".next", ".cache", ".DS_Store"]
    )
    max_file_size: int = 524288


class ProjectStats(BaseModel):
    total_files: int = 0
    indexed_files: int = 0
    total_chunks: int = 0
    total_symbols: int = 0


class ProjectCreate(BaseModel):
    host_path: str


class ProjectUpdate(BaseModel):
    settings: ProjectSettings | None = None


class ProjectResponse(BaseModel):
    host_path: str
    container_path: str
    languages: list[str] = Field(default_factory=list)
    settings: ProjectSettings = Field(default_factory=ProjectSettings)
    stats: ProjectStats = Field(default_factory=ProjectStats)
    status: str = "created"
    created_at: datetime
    updated_at: datetime
    last_indexed_at: datetime | None = None


class ProjectListResponse(BaseModel):
    projects: list[ProjectResponse]
    total: int
