#!/usr/bin/env python3
"""
Benchmark GGUF embedding quality against fp16 sentence-transformers baseline.

Validates the claim that the Q8_0 GGUF build of CodeRankEmbed has negligible
retrieval-quality loss compared to the fp16 reference. Reports Jaccard@k,
Recall@k, and rank-correlation (Kendall tau) on a fixed query set run against
a local code corpus (defaults to this repository).

Install before running:
  uv pip install sentence-transformers torch einops  # fp16 reference
  uv pip install llama-cpp-python huggingface-hub    # already in requirements.txt

Usage:
  python scripts/benchmark_embeddings.py \
      --corpus . \
      --gguf-repo awhiteside/CodeRankEmbed-Q8_0-GGUF \
      --fp16-repo nomic-ai/CodeRankEmbed \
      --k 10 \
      --output doc/benchmark-q8-vs-fp16.md

Acceptance thresholds:
  Jaccard@10 >= 0.7
  Recall@10  >= 0.9
  Kendall tau >= 0.5
"""
from __future__ import annotations

import argparse
import json
import logging
import math
import os
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("benchmark")

QUERIES: list[str] = [
    "async queue timeout",
    "parse tree-sitter chunk",
    "chroma collection upsert",
    "cli root command version",
    "embedding service load model",
    "project root detection",
    "file watcher branch switch",
    "config yaml migration legacy keys",
    "indexing status estimated finish",
    "search by meaning code",
    "api key authentication middleware",
    "health endpoint status response",
    "docker compose cuda healthcheck",
    "gitignore pattern matching",
    "sqlite projects table schema",
    "mean pooling embedding",
    "batch size inference throughput",
    "incremental reindex sha256",
    "client version header compatibility",
    "goroutine concurrent walk",
]

CODE_EXTENSIONS = {".py", ".go", ".js", ".ts", ".rs", ".java", ".cpp", ".c", ".h"}
MAX_CHUNK_CHARS = 2000
EXCLUDE_DIRS = {".git", ".venv", "node_modules", "build", "dist", "__pycache__", "data"}
QUERY_PREFIX = "Represent this query for searching relevant code: "


@dataclass
class Chunk:
    chunk_id: str  # "relative/path.py:0"
    path: str
    content: str


@dataclass
class BackendResult:
    name: str
    load_seconds: float = 0.0
    embed_seconds: float = 0.0
    dim: int = 0
    top_k: dict[str, list[str]] = field(default_factory=dict)  # query -> chunk_ids


def collect_chunks(corpus_root: Path) -> list[Chunk]:
    chunks: list[Chunk] = []
    for path in corpus_root.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix not in CODE_EXTENSIONS:
            continue
        if any(part in EXCLUDE_DIRS for part in path.parts):
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        if not text.strip():
            continue
        rel = path.relative_to(corpus_root).as_posix()
        # Slice to ≤MAX_CHUNK_CHARS chunks, line-aligned where possible.
        if len(text) <= MAX_CHUNK_CHARS:
            chunks.append(Chunk(f"{rel}:0", rel, text))
            continue
        idx = 0
        part = 0
        while idx < len(text):
            end = min(idx + MAX_CHUNK_CHARS, len(text))
            # extend to next newline to avoid slicing mid-token
            nl = text.find("\n", end)
            if nl != -1 and nl - end < 200:
                end = nl + 1
            chunks.append(Chunk(f"{rel}:{part}", rel, text[idx:end]))
            idx = end
            part += 1
    return chunks


def cosine(a: list[float], b: list[float]) -> float:
    # Fast enough on pure-Python for a few thousand vectors * 20 queries.
    num = sum(x * y for x, y in zip(a, b))
    da = math.sqrt(sum(x * x for x in a))
    db = math.sqrt(sum(y * y for y in b))
    if da == 0 or db == 0:
        return 0.0
    return num / (da * db)


def top_k_per_query(
    chunk_vecs: dict[str, list[float]],
    query_vecs: dict[str, list[float]],
    k: int,
) -> dict[str, list[str]]:
    result: dict[str, list[str]] = {}
    for q, qv in query_vecs.items():
        scored = [(cid, cosine(qv, cv)) for cid, cv in chunk_vecs.items()]
        scored.sort(key=lambda x: x[1], reverse=True)
        result[q] = [cid for cid, _ in scored[:k]]
    return result


def run_fp16(
    chunks: list[Chunk],
    queries: list[str],
    repo: str,
) -> BackendResult:
    from sentence_transformers import SentenceTransformer  # type: ignore

    t0 = time.monotonic()
    model = SentenceTransformer(repo, trust_remote_code=True)
    load_s = time.monotonic() - t0

    t0 = time.monotonic()
    chunk_embeddings = model.encode(
        [c.content for c in chunks], show_progress_bar=True, batch_size=8
    ).tolist()
    query_embeddings = model.encode(
        [QUERY_PREFIX + q for q in queries], show_progress_bar=False
    ).tolist()
    embed_s = time.monotonic() - t0

    chunk_vecs = {c.chunk_id: v for c, v in zip(chunks, chunk_embeddings)}
    query_vecs = dict(zip(queries, query_embeddings))
    return BackendResult(
        name=f"fp16/{repo}",
        load_seconds=load_s,
        embed_seconds=embed_s,
        dim=len(chunk_embeddings[0]) if chunk_embeddings else 0,
        top_k=top_k_per_query(chunk_vecs, query_vecs, 10),
    )


def run_gguf(
    chunks: list[Chunk],
    queries: list[str],
    repo: str,
    gguf_filename: str | None = None,
) -> BackendResult:
    from huggingface_hub import hf_hub_download, list_repo_files  # type: ignore
    from llama_cpp import Llama  # type: ignore

    t0 = time.monotonic()
    files = list(list_repo_files(repo))
    if gguf_filename:
        gguf_file = gguf_filename if gguf_filename in files else None
        if not gguf_file:
            raise RuntimeError(f"File {gguf_filename} not found in {repo}. Available: {[f for f in files if f.endswith('.gguf')]}")
    else:
        gguf_file = next((f for f in files if f.endswith(".gguf")), None)
    if not gguf_file:
        raise RuntimeError(f"No .gguf file in {repo}")
    model_path = hf_hub_download(repo_id=repo, filename=gguf_file)

    n_gpu_layers = int(os.environ.get("CIX_N_GPU_LAYERS", "-1"))
    # n_ctx matches production config (max_chunk_tokens=1500 + 128 headroom)
    model = Llama(
        model_path=model_path,
        embedding=True,
        n_ctx=1628,
        n_gpu_layers=n_gpu_layers,
        verbose=False,
    )
    load_s = time.monotonic() - t0

    t0 = time.monotonic()
    # Embed one text at a time to avoid context-window overflow across chunks
    chunk_vecs: dict[str, list[float]] = {}
    for i, c in enumerate(chunks):
        result = model.create_embedding([c.content])
        chunk_vecs[c.chunk_id] = result["data"][0]["embedding"]
        if (i + 1) % 50 == 0:
            logger.info("  GGUF embedded %d/%d chunks", i + 1, len(chunks))
    query_vecs: dict[str, list[float]] = {}
    for q in queries:
        result = model.create_embedding([QUERY_PREFIX + q])
        query_vecs[q] = result["data"][0]["embedding"]
    embed_s = time.monotonic() - t0

    # derive dim from first embedding
    first_vec = next(iter(chunk_vecs.values()), [])
    dim = len(first_vec)
    return BackendResult(
        name=f"gguf/{repo}/{gguf_filename or 'auto'}",
        load_seconds=load_s,
        embed_seconds=embed_s,
        dim=dim,
        top_k=top_k_per_query(chunk_vecs, query_vecs, 10),
    )


def jaccard(a: list[str], b: list[str]) -> float:
    sa, sb = set(a), set(b)
    if not sa and not sb:
        return 1.0
    return len(sa & sb) / len(sa | sb)


def recall_at_k(reference: list[str], candidate: list[str]) -> float:
    if not reference:
        return 1.0
    hits = sum(1 for item in reference if item in candidate)
    return hits / len(reference)


def kendall_tau(reference: list[str], candidate: list[str]) -> float:
    # Rank-correlation restricted to items that appear in both lists.
    common = [item for item in reference if item in candidate]
    if len(common) < 2:
        return 1.0 if len(common) == len(reference) else 0.0
    ref_rank = {item: i for i, item in enumerate(reference)}
    cand_rank = {item: i for i, item in enumerate(candidate)}
    concordant = discordant = 0
    for i in range(len(common)):
        for j in range(i + 1, len(common)):
            a, b = common[i], common[j]
            ra, rb = ref_rank[a] - ref_rank[b], cand_rank[a] - cand_rank[b]
            if ra * rb > 0:
                concordant += 1
            elif ra * rb < 0:
                discordant += 1
    total = concordant + discordant
    return (concordant - discordant) / total if total else 0.0


def write_report(
    output: Path,
    reference: BackendResult,
    candidate: BackendResult,
    k: int,
    raw_path: Path,
) -> dict[str, float]:
    per_query = []
    jaccards: list[float] = []
    recalls: list[float] = []
    taus: list[float] = []
    for q in reference.top_k:
        ref = reference.top_k[q]
        cand = candidate.top_k.get(q, [])
        j = jaccard(ref, cand)
        r = recall_at_k(ref, cand)
        t = kendall_tau(ref, cand)
        jaccards.append(j)
        recalls.append(r)
        taus.append(t)
        per_query.append((q, j, r, t))

    def mean(xs: list[float]) -> float:
        return sum(xs) / len(xs) if xs else 0.0

    summary = {
        "jaccard_mean": mean(jaccards),
        "recall_mean": mean(recalls),
        "kendall_tau_mean": mean(taus),
        "reference_embed_seconds": reference.embed_seconds,
        "candidate_embed_seconds": candidate.embed_seconds,
        "speedup": (
            reference.embed_seconds / candidate.embed_seconds
            if candidate.embed_seconds > 0
            else 0.0
        ),
    }

    lines: list[str] = []
    lines.append(f"# Embedding Quality Benchmark — {candidate.name} vs {reference.name}\n")
    lines.append("")
    lines.append(f"**k** = {k}  |  **queries** = {len(reference.top_k)}  |  **dim ref/cand** = {reference.dim}/{candidate.dim}")
    lines.append("")
    lines.append("## Summary")
    lines.append("")
    lines.append("| Metric | Value | Acceptance |")
    lines.append("|---|---:|---:|")
    lines.append(f"| Jaccard@{k} (mean) | {summary['jaccard_mean']:.3f} | ≥ 0.70 |")
    lines.append(f"| Recall@{k} (mean) | {summary['recall_mean']:.3f} | ≥ 0.90 |")
    lines.append(f"| Kendall tau (mean) | {summary['kendall_tau_mean']:.3f} | ≥ 0.50 |")
    lines.append(f"| Reference embed time | {reference.embed_seconds:.1f}s | — |")
    lines.append(f"| Candidate embed time | {candidate.embed_seconds:.1f}s | — |")
    lines.append(f"| Speedup (ref/cand) | {summary['speedup']:.2f}× | — |")
    lines.append("")
    lines.append("## Per-query scores")
    lines.append("")
    lines.append("| Query | Jaccard | Recall | Kendall τ |")
    lines.append("|---|---:|---:|---:|")
    for q, j, r, t in per_query:
        lines.append(f"| `{q}` | {j:.3f} | {r:.3f} | {t:.3f} |")
    lines.append("")
    lines.append(f"Raw top-k lists: `{raw_path.name}`")
    lines.append("")

    output.write_text("\n".join(lines), encoding="utf-8")
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--corpus", type=Path, default=Path.cwd(),
                        help="Directory to index (default: CWD)")
    parser.add_argument("--gguf-repo", default="awhiteside/CodeRankEmbed-Q8_0-GGUF")
    parser.add_argument("--gguf-file", default=None,
                        help="Specific .gguf filename to use from the repo (optional)")
    parser.add_argument("--fp16-repo", default="nomic-ai/CodeRankEmbed")
    parser.add_argument("--fp16-cache", type=Path, default=None,
                        help="Path to JSON file for caching/loading fp16 results. "
                             "If file exists, load from it; otherwise run fp16 and save.")
    parser.add_argument("--k", type=int, default=10)
    parser.add_argument("--output", type=Path, default=Path("doc/benchmark-q8-vs-fp16.md"))
    parser.add_argument("--skip-fp16", action="store_true",
                        help="Skip fp16 reference — useful for quick sanity checks")
    args = parser.parse_args()

    logger.info("Collecting chunks from %s", args.corpus)
    chunks = collect_chunks(args.corpus)
    logger.info("Collected %d chunks", len(chunks))
    if not chunks:
        logger.error("No chunks to benchmark")
        return 1

    logger.info("Running GGUF backend: %s (file: %s)", args.gguf_repo, args.gguf_file or "auto")
    gguf = run_gguf(chunks, QUERIES, args.gguf_repo, gguf_filename=args.gguf_file)

    if args.skip_fp16:
        logger.info("Skipping fp16 reference (--skip-fp16)")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        raw_dir = args.output.parent / "benchmark-data"
        raw_dir.mkdir(parents=True, exist_ok=True)
        raw = raw_dir / (args.output.stem + ".json")
        raw.write_text(json.dumps({"gguf": gguf.top_k}, indent=2), encoding="utf-8")
        logger.info("Wrote top-k to %s (no comparison possible)", raw)
        return 0

    # fp16 caching: load from cache file if available, else run and save
    fp16: BackendResult
    if args.fp16_cache and args.fp16_cache.exists():
        logger.info("Loading fp16 results from cache: %s", args.fp16_cache)
        cache_data = json.loads(args.fp16_cache.read_text(encoding="utf-8"))
        fp16 = BackendResult(
            name=cache_data["name"],
            load_seconds=cache_data["load_seconds"],
            embed_seconds=cache_data["embed_seconds"],
            dim=cache_data["dim"],
            top_k=cache_data["top_k"],
        )
    else:
        logger.info("Running fp16 reference backend: %s", args.fp16_repo)
        fp16 = run_fp16(chunks, QUERIES, args.fp16_repo)
        if args.fp16_cache:
            args.fp16_cache.parent.mkdir(parents=True, exist_ok=True)
            cache_payload = {
                "name": fp16.name,
                "load_seconds": fp16.load_seconds,
                "embed_seconds": fp16.embed_seconds,
                "dim": fp16.dim,
                "top_k": fp16.top_k,
            }
            args.fp16_cache.write_text(json.dumps(cache_payload, indent=2), encoding="utf-8")
            logger.info("Saved fp16 cache to %s", args.fp16_cache)

    args.output.parent.mkdir(parents=True, exist_ok=True)
    raw_dir = args.output.parent / "benchmark-data"
    raw_dir.mkdir(parents=True, exist_ok=True)
    raw_path = raw_dir / (args.output.stem + ".json")
    raw_path.write_text(
        json.dumps({"fp16": fp16.top_k, "gguf": gguf.top_k}, indent=2),
        encoding="utf-8",
    )
    summary = write_report(args.output, fp16, gguf, args.k, raw_path)

    logger.info("Summary: %s", summary)
    logger.info("Report written to %s", args.output)

    failed = []
    if summary["jaccard_mean"] < 0.7:
        failed.append(f"Jaccard {summary['jaccard_mean']:.3f} < 0.70")
    if summary["recall_mean"] < 0.9:
        failed.append(f"Recall {summary['recall_mean']:.3f} < 0.90")
    if summary["kendall_tau_mean"] < 0.5:
        failed.append(f"Kendall τ {summary['kendall_tau_mean']:.3f} < 0.50")
    if failed:
        logger.error("Acceptance criteria failed: %s", "; ".join(failed))
        return 2
    logger.info("All acceptance criteria passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
