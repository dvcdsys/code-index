import asyncio
import gc
import logging
import os
import time as _time
from concurrent.futures import ThreadPoolExecutor

from ..config import settings

logger = logging.getLogger(__name__)

_AVG_BATCH_SEC_DEFAULT = 3.0
_EMA_ALPHA = 0.25

# Adaptive batch size limits measured on nomic-ai/CodeRankEmbed (RTX 3090).
# Each tuple is (max_seq_len_tokens, max_batch_size) that keeps peak VRAM
# under ~4500 MB — leaving the model (~650 MB) + safety margin within 5 GB.
# 1 token ≈ 4 ASCII chars; we estimate tokens from avg character length.
_BATCH_LIMITS: list[tuple[int, int]] = [
    (256,  8),   # peak ≤  692 MB
    (512,  8),   # peak ≤  985 MB
    (1024, 8),   # peak ≤ 2127 MB
    (2048, 4),   # peak ≤ 4077 MB  (bs=8 → 7607 MB, OOM)
    (4096, 1),   # peak ≤ 4402 MB  (bs=2 → 8257 MB, OOM)
    (8192, 1),   # likely OOM even at bs=1; chunker should avoid 8k-token chunks
]


def _safe_batch_size(avg_chars: float) -> int:
    """Return the largest batch size that keeps peak VRAM under ~5 GB.

    Estimated token count = avg_chars / 4 (1 token ≈ 4 ASCII chars).
    Capped by settings.max_batch_size (MAX_BATCH_SIZE env var).
    Falls back to 1 for any sequence length beyond the profiled range.
    """
    est_tokens = int(avg_chars / 4)
    for max_tokens, max_bs in _BATCH_LIMITS:
        if est_tokens <= max_tokens:
            return min(max_bs, settings.max_batch_size)
    return 1

# Models that require a query prefix for asymmetric retrieval
QUERY_PREFIX_MODELS = {
    "nomic-ai/CodeRankEmbed": "Represent this query for searching relevant code: ",
    "nomic-ai/nomic-embed-text-v1.5": "search_query: ",
    "BAAI/bge-base-en-v1.5": "Represent this sentence for searching relevant passages: ",
    "BAAI/bge-large-en-v1.5": "Represent this sentence for searching relevant passages: ",
}


class EmbeddingBusyError(RuntimeError):
    """Raised when the GPU queue is full and the request timed out waiting.

    Attributes:
        retry_after: suggested seconds the caller should wait before retrying.
    """

    def __init__(self, message: str, retry_after: int = 5) -> None:
        super().__init__(message)
        self.retry_after = retry_after


def _clear_torch_cache():
    """Free PyTorch memory caches."""
    try:
        import torch
        if hasattr(torch, "mps") and torch.backends.mps.is_available():
            torch.mps.empty_cache()
        if torch.cuda.is_available():
            torch.cuda.empty_cache()
    except Exception:
        pass


class EmbeddingService:
    def __init__(self):
        self._model = None
        # ThreadPoolExecutor threads == GPU slots so inference never queues
        # inside the executor while a semaphore slot is free outside it.
        self._executor = ThreadPoolExecutor(
            max_workers=max(1, settings.max_embedding_concurrency)
        )
        self._query_prefix = ""

        # Limits concurrent GPU embedding sessions to prevent CUDA OOM from
        # memory-allocator fragmentation caused by multiple large files.
        # HTTP requests beyond the limit suspend in the asyncio event loop
        # (non-blocking) until a slot is freed or embedding_queue_timeout elapses.
        self._semaphore = asyncio.Semaphore(settings.max_embedding_concurrency)

        # EMA of per-batch inference time; drives the estimated-finish calculation.
        # Stored in-memory only — resets on restart, converges after a few calls.
        self._avg_batch_sec: float = _AVG_BATCH_SEC_DEFAULT
        # Monotonic deadline of the currently running embedding; 0 when idle.
        self._estimated_finish_at: float = 0.0

    async def load_model(self):
        loop = asyncio.get_event_loop()
        self._model = await loop.run_in_executor(
            self._executor, self._load_model_sync
        )
        self._query_prefix = QUERY_PREFIX_MODELS.get(settings.embedding_model, "")
        logger.info(
            "Embedding model loaded: %s (dims=%d, query_prefix=%r)",
            settings.embedding_model,
            self._model.get_sentence_embedding_dimension(),
            self._query_prefix,
        )

    def _load_model_sync(self):
        os.environ["TOKENIZERS_PARALLELISM"] = "false"
        os.environ.setdefault("OMP_NUM_THREADS", str(os.cpu_count() or 2))

        import torch
        from sentence_transformers import SentenceTransformer

        if hasattr(torch.backends, "mps") and torch.backends.mps.is_available():
            device = "mps"
        elif torch.cuda.is_available():
            device = "cuda"
        else:
            device = "cpu"

        logger.info("Loading model on device: %s", device)

        return SentenceTransformer(
            settings.embedding_model,
            trust_remote_code=True,
            device=device,
        )

    async def embed_texts(self, texts: list[str]) -> list[list[float]]:
        """Embed a list of texts, waiting in the GPU queue if all slots are busy.

        Multiple HTTP requests run concurrently in the asyncio event loop
        (chunking, DB writes, etc.) — only the GPU step is serialised here.
        When all max_embedding_concurrency slots are taken the coroutine
        suspends (non-blocking) until a slot frees or embedding_queue_timeout
        seconds elapse, at which point EmbeddingBusyError is raised and the
        router returns HTTP 503 with Retry-After so the Go client can back off.
        """
        if not self._model:
            raise RuntimeError("Model not loaded")

        timeout = settings.embedding_queue_timeout
        try:
            # asyncio.timeout(0) fires on the first yield when the semaphore is
            # taken — "reject immediately" semantics for timeout=0.
            async with asyncio.timeout(timeout if timeout > 0 else 0):
                async with self._semaphore:
                    return await self._embed_locked(texts)
        except TimeoutError:
            retry_after = max(5, int(self._estimated_finish_at - _time.monotonic()))
            raise EmbeddingBusyError(
                f"GPU queue is full — request waited {timeout}s without a free slot",
                retry_after=retry_after,
            )

    async def _embed_locked(self, texts: list[str]) -> list[list[float]]:
        """Run embedding with the semaphore already held.

        Batch size is chosen dynamically based on average character length of
        the texts so that peak VRAM stays within ~5 GB on a single GPU.
        """
        avg_chars = sum(len(t) for t in texts) / max(1, len(texts))
        batch_size = _safe_batch_size(avg_chars)
        if batch_size != 4:  # log only non-default choices
            logger.debug(
                "adaptive batch_size=%d for avg_chars=%.0f (~%d tokens)",
                batch_size, avg_chars, int(avg_chars / 4),
            )

        n_batches = max(1, (len(texts) + batch_size - 1) // batch_size)
        self._estimated_finish_at = _time.monotonic() + n_batches * self._avg_batch_sec

        all_embeddings: list[list[float]] = []
        loop = asyncio.get_event_loop()
        texts_remaining = len(texts)

        for i in range(0, len(texts), batch_size):
            batch = texts[i:i + batch_size]
            t0 = _time.monotonic()

            embeddings = await loop.run_in_executor(
                self._executor,
                lambda b=batch: self._encode_and_convert(b),
            )

            batch_sec = _time.monotonic() - t0
            self._avg_batch_sec = (
                (1 - _EMA_ALPHA) * self._avg_batch_sec + _EMA_ALPHA * batch_sec
            )
            texts_remaining = max(0, texts_remaining - len(batch))
            rem_batches = (texts_remaining + batch_size - 1) // batch_size
            self._estimated_finish_at = (
                _time.monotonic() + rem_batches * self._avg_batch_sec
            )

            all_embeddings.extend(embeddings)

        return all_embeddings

    def _encode_and_convert(self, texts: list[str]) -> list[list[float]]:
        result = self._model.encode(texts, show_progress_bar=False)
        converted = result.tolist()
        del result
        _clear_torch_cache()
        return converted

    async def embed_query(self, query: str) -> list[float]:
        if not self._model:
            raise RuntimeError("Model not loaded")

        prefixed_query = self._query_prefix + query

        loop = asyncio.get_event_loop()
        embedding = await loop.run_in_executor(
            self._executor,
            lambda: self._model.encode(prefixed_query, show_progress_bar=False).tolist(),
        )
        return embedding


embedding_service = EmbeddingService()