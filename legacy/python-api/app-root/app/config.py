import os
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    api_key: str = ""
    port: int = 21847
    embedding_model: str = "awhiteside/CodeRankEmbed-Q8_0-GGUF"
    chroma_persist_dir: str = "/data/chroma"
    sqlite_path: str = "/data/sqlite/projects.db"
    max_file_size: int = 524288
    excluded_dirs: str = "node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store"

    @property
    def model_safe_name(self) -> str:
        return self.embedding_model.replace("/", "_").replace("-", "_").lower()

    @property
    def dynamic_chroma_persist_dir(self) -> str:
        return f"{self.chroma_persist_dir}_{self.model_safe_name}"

    @property
    def dynamic_sqlite_path(self) -> str:
        base, ext = os.path.splitext(self.sqlite_path)
        return f"{base}_{self.model_safe_name}{ext}"

    # Concurrent embedding calls. llama-cpp-python holds a single context per Llama
    # instance, so parallel create_embedding() calls on the same model serialize
    # anyway. Keep at 1 unless you instantiate separate models.
    max_embedding_concurrency: int = 1

    # Seconds an /index/files request waits for a free embedding slot before the
    # server returns HTTP 503 with Retry-After (the Go client auto-retries).
    # 0 = reject immediately.
    embedding_queue_timeout: int = 300

    # Maximum chunk length in tokens. 1 token ≈ 4 ASCII chars.
    # The chunker enforces this via MAX_CHUNK_SIZE = max_chunk_tokens * 4.
    # Also drives n_ctx for the llama.cpp context buffer.
    max_chunk_tokens: int = 1500

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
