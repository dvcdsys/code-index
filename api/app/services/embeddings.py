import asyncio
import gc
import logging
import os
from concurrent.futures import ThreadPoolExecutor

from ..config import settings

logger = logging.getLogger(__name__)

BATCH_SIZE = 4

# Models that require a query prefix for asymmetric retrieval
QUERY_PREFIX_MODELS = {
    "nomic-ai/CodeRankEmbed": "Represent this query for searching relevant code: ",
    "nomic-ai/nomic-embed-text-v1.5": "search_query: ",
    "BAAI/bge-base-en-v1.5": "Represent this sentence for searching relevant passages: ",
    "BAAI/bge-large-en-v1.5": "Represent this sentence for searching relevant passages: ",
}


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
        self._executor = ThreadPoolExecutor(max_workers=1)
        self._query_prefix = ""

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

        # Auto-detect best device: MPS (Apple GPU) > CUDA > CPU
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
        if not self._model:
            raise RuntimeError("Model not loaded")

        all_embeddings = []
        loop = asyncio.get_event_loop()

        for i in range(0, len(texts), BATCH_SIZE):
            batch = texts[i:i + BATCH_SIZE]
            embeddings = await loop.run_in_executor(
                self._executor,
                lambda b=batch: self._encode_and_convert(b),
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
