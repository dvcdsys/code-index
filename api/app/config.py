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
