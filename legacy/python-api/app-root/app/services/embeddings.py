import asyncio
import logging
import os
import platform
import subprocess
import time as _time
from concurrent.futures import ThreadPoolExecutor
from typing import Any

from ..config import settings

logger = logging.getLogger(__name__)

_AVG_BATCH_SEC_DEFAULT = 3.0
_EMA_ALPHA = 0.25

# Models that require a query prefix for asymmetric retrieval.
QUERY_PREFIX_MODELS = {
    "nomic-ai/CodeRankEmbed": "Represent this query for searching relevant code: ",
    "nomic-ai/nomic-embed-text-v1.5": "search_query: ",
    "BAAI/bge-base-en-v1.5": "Represent this sentence for searching relevant passages: ",
    "BAAI/bge-large-en-v1.5": "Represent this sentence for searching relevant passages: ",
    "awhiteside/CodeRankEmbed-Q8_0-GGUF": "Represent this query for searching relevant code: ",
}


def _resolve_query_prefix(model_name: str) -> str:
    if model_name in QUERY_PREFIX_MODELS:
        return QUERY_PREFIX_MODELS[model_name]
    lowered = model_name.lower()
    if "coderankembed" in lowered:
        return QUERY_PREFIX_MODELS["nomic-ai/CodeRankEmbed"]
    if "nomic-embed-text" in lowered:
        return QUERY_PREFIX_MODELS["nomic-ai/nomic-embed-text-v1.5"]
    if "bge-base" in lowered:
        return QUERY_PREFIX_MODELS["BAAI/bge-base-en-v1.5"]
    if "bge-large" in lowered:
        return QUERY_PREFIX_MODELS["BAAI/bge-large-en-v1.5"]
    return ""


def _detect_gpu_layers() -> int:
    # Explicit override wins — e.g. CIX_N_GPU_LAYERS=0 forces CPU on a GPU box.
    explicit = os.environ.get("CIX_N_GPU_LAYERS")
    if explicit is not None:
        return int(explicit)
    # macOS: llama-cpp-python pip wheel ships with Metal enabled.
    if platform.system() == "Darwin":
        return -1
    # Linux: if nvidia-smi responds, llama.cpp was built against CUDA (Dockerfile.cuda).
    try:
        subprocess.run(
            ["nvidia-smi"],
            capture_output=True,
            timeout=1,
            check=True,
        )
        return -1
    except (FileNotFoundError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return 0


class EmbeddingBusyError(RuntimeError):
    """Raised when the embedding queue is full and the request timed out waiting."""

    def __init__(self, message: str, retry_after: int = 5) -> None:
        super().__init__(message)
        self.retry_after = retry_after


class EmbeddingService:
    def __init__(self):
        self._model: Any = None
        self._executor = ThreadPoolExecutor(
            max_workers=max(1, settings.max_embedding_concurrency)
        )
        self._query_prefix = ""
        self._semaphore = asyncio.Semaphore(settings.max_embedding_concurrency)
        self._avg_batch_sec: float = _AVG_BATCH_SEC_DEFAULT
        self._estimated_finish_at: float = 0.0

    async def load_model(self):
        loop = asyncio.get_event_loop()
        self._model = await loop.run_in_executor(
            self._executor, self._load_model_sync
        )
        self._query_prefix = _resolve_query_prefix(settings.embedding_model)

        logger.info(
            "Embedding model loaded: %s (dims=%d, query_prefix=%r)",
            settings.embedding_model,
            self._model.n_embd(),
            self._query_prefix,
        )

    def _load_model_sync(self):
        os.environ["TOKENIZERS_PARALLELISM"] = "false"
        os.environ.setdefault("OMP_NUM_THREADS", str(os.cpu_count() or 2))

        from huggingface_hub import hf_hub_download, list_repo_files
        from llama_cpp import Llama

        model_path = settings.embedding_model

        if "/" in model_path and not os.path.exists(model_path):
            logger.info("Downloading GGUF model from Hugging Face: %s", model_path)
            files = list_repo_files(model_path)
            gguf_file = next((f for f in files if f.endswith(".gguf")), None)
            if not gguf_file:
                raise ValueError(
                    f"No .gguf file found in repo {model_path}. "
                    "Only GGUF repositories are supported."
                )
            model_path = hf_hub_download(repo_id=model_path, filename=gguf_file)

        n_gpu_layers = _detect_gpu_layers()
        logger.info(
            "Loading Llama (n_ctx=%d, n_gpu_layers=%d)",
            settings.max_chunk_tokens + 128,
            n_gpu_layers,
        )

        return Llama(
            model_path=model_path,
            embedding=True,
            n_ctx=settings.max_chunk_tokens + 128,
            n_threads=int(os.environ.get("OMP_NUM_THREADS", "4")),
            n_gpu_layers=n_gpu_layers,
            verbose=False,
        )

    async def embed_texts(self, texts: list[str]) -> list[list[float]]:
        if not self._model:
            raise RuntimeError("Model not loaded")

        timeout = settings.embedding_queue_timeout
        try:
            async with asyncio.timeout(timeout if timeout > 0 else 0):
                async with self._semaphore:
                    return await self._embed_locked(texts)
        except TimeoutError:
            retry_after = max(5, int(self._estimated_finish_at - _time.monotonic()))
            raise EmbeddingBusyError(
                f"Queue is full — request waited {timeout}s without a free slot",
                retry_after=retry_after,
            )

    async def _embed_locked(self, texts: list[str]) -> list[list[float]]:
        if not texts:
            return []

        self._estimated_finish_at = _time.monotonic() + self._avg_batch_sec
        loop = asyncio.get_event_loop()
        t0 = _time.monotonic()

        result = await loop.run_in_executor(
            self._executor,
            lambda: self._model.create_embedding(texts),
        )

        batch_sec = _time.monotonic() - t0
        self._avg_batch_sec = (
            (1 - _EMA_ALPHA) * self._avg_batch_sec + _EMA_ALPHA * batch_sec
        )
        self._estimated_finish_at = 0.0

        logger.debug("Embedded %d texts in %.2fs", len(texts), batch_sec)
        return [item["embedding"] for item in result["data"]]

    async def embed_query(self, query: str) -> list[float]:
        if not self._model:
            raise RuntimeError("Model not loaded")

        prefixed_query = self._query_prefix + query
        loop = asyncio.get_event_loop()

        result = await loop.run_in_executor(
            self._executor,
            lambda: self._model.create_embedding(prefixed_query),
        )
        return result["data"][0]["embedding"]


embedding_service = EmbeddingService()
