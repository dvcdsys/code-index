import hashlib
import logging

import chromadb

from ..config import settings

logger = logging.getLogger(__name__)


class VectorStoreService:
    def __init__(self):
        self._client: chromadb.ClientAPI | None = None

    def init(self):
        self._client = chromadb.PersistentClient(path=settings.chroma_persist_dir)
        logger.info("ChromaDB initialized at %s", settings.chroma_persist_dir)

    @property
    def client(self) -> chromadb.ClientAPI:
        if self._client is None:
            self.init()
        return self._client

    def _collection_name(self, project_path: str) -> str:
        # Use hash of path to create valid collection name
        path_hash = hashlib.md5(project_path.encode()).hexdigest()
        return f"project_{path_hash}"

    def get_or_create_collection(self, project_path: str) -> chromadb.Collection:
        return self.client.get_or_create_collection(
            name=self._collection_name(project_path),
            metadata={"hnsw:space": "cosine"},
        )

    async def upsert_chunks(
        self,
        project_path: str,
        chunks: list,
        embeddings: list[list[float]],
    ):
        collection = self.get_or_create_collection(project_path)

        ids = []
        documents = []
        metadatas = []
        embs = []

        for idx, (chunk, embedding) in enumerate(zip(chunks, embeddings)):
            path_hash = hashlib.md5(chunk.file_path.encode()).hexdigest()[:12]
            doc_id = f"{path_hash}:{chunk.start_line}-{chunk.end_line}:{idx}"

            ids.append(doc_id)
            documents.append(chunk.content)
            metadatas.append({
                "file_path": chunk.file_path,
                "start_line": chunk.start_line,
                "end_line": chunk.end_line,
                "chunk_type": chunk.chunk_type,
                "symbol_name": chunk.symbol_name or "",
                "language": chunk.language,
            })
            embs.append(embedding)

        # Upsert in batches of 500 (ChromaDB limit)
        batch_size = 500
        for i in range(0, len(ids), batch_size):
            end = i + batch_size
            collection.upsert(
                ids=ids[i:end],
                documents=documents[i:end],
                metadatas=metadatas[i:end],
                embeddings=embs[i:end],
            )

    async def search(
        self,
        project_path: str,
        query_embedding: list[float],
        limit: int = 10,
        where: dict | None = None,
    ) -> list[dict]:
        collection = self.get_or_create_collection(project_path)

        kwargs = {
            "query_embeddings": [query_embedding],
            "n_results": limit,
            "include": ["documents", "metadatas", "distances"],
        }
        if where:
            kwargs["where"] = where

        try:
            results = collection.query(**kwargs)
        except Exception as e:
            logger.error("ChromaDB search error: %s", e)
            return []

        items = []
        if results and results["ids"] and results["ids"][0]:
            for i in range(len(results["ids"][0])):
                metadata = results["metadatas"][0][i]
                distance = results["distances"][0][i]
                # Cosine distance to similarity score
                score = 1.0 - distance

                items.append({
                    "file_path": metadata["file_path"],
                    "start_line": metadata["start_line"],
                    "end_line": metadata["end_line"],
                    "content": results["documents"][0][i],
                    "score": round(score, 4),
                    "chunk_type": metadata["chunk_type"],
                    "symbol_name": metadata.get("symbol_name", ""),
                    "language": metadata.get("language", ""),
                })

        return items

    async def delete_by_file(self, project_path: str, file_path: str):
        collection = self.get_or_create_collection(project_path)
        try:
            collection.delete(where={"file_path": file_path})
        except Exception as e:
            logger.warning("Failed to delete chunks for %s: %s", file_path, e)

    def delete_collection(self, project_path: str):
        name = self._collection_name(project_path)
        try:
            self.client.delete_collection(name)
        except Exception:
            pass


vector_store_service = VectorStoreService()
