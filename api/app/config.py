import os
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    api_key: str = ""
    port: int = 21847
    embedding_model: str = "nomic-ai/CodeRankEmbed"
    chroma_persist_dir: str = "/data/chroma"
    sqlite_path: str = "/data/sqlite/projects.db"
    max_file_size: int = 524288
    excluded_dirs: str = "node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store"

    # How many embedding calls may run on the GPU simultaneously.
    # Keep at 1 for a single GPU — prevents CUDA OOM from allocator fragmentation
    # when multiple large files are indexed concurrently. Increase only if you have
    # confirmed spare VRAM (e.g. multiple GPUs or small average chunk sizes).
    max_embedding_concurrency: int = 1

    # Seconds a /index/files request will wait in the GPU queue before the server
    # returns HTTP 503 with Retry-After. The Go client retries automatically.
    # 0 = reject immediately when all GPU slots are occupied.
    embedding_queue_timeout: int = 300

    # Maximum chunk length in tokens sent to the embedding model.
    # Controls peak VRAM: smaller values = lower memory, less context per chunk.
    # 1 token ≈ 4 ASCII chars. The chunker enforces this via MAX_CHUNK_SIZE = max_chunk_tokens * 4.
    # See doc/vram-profiling.md for the full VRAM table.
    max_chunk_tokens: int = 1500

    # Hard cap on the batch size selected by _safe_batch_size().
    # Lower this to reduce peak VRAM at the cost of indexing throughput.
    # The default (8) lets _safe_batch_size() use the full _BATCH_LIMITS table.
    max_batch_size: int = 8


    model_config = SettingsConfigDict(
        env_file=os.path.join(os.path.dirname(__file__), "../../.env"),
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
    )

    @property
    def excluded_dirs_list(self) -> list[str]:
        return [d.strip() for d in self.excluded_dirs.split(",") if d.strip()]


settings = Settings()
