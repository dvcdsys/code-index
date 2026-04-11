#!/usr/bin/env python3
"""
VRAM profiling for the embedding model.

Measures peak GPU memory per (batch_size, seq_len) combination to determine
the relationship between chunk sizes and VRAM usage. Run this with the
indexing server STOPPED so measurements are clean.

Usage on the server:
  docker compose -f /path/to/stack/docker-compose.yml stop code-index-api
  docker run --rm --gpus all \
      -e EMBEDDING_MODEL=nomic-ai/CodeRankEmbed \
      -v cix_cix_data:/data \
      dvcdsys/code-index:test-cu130 \
      python3 /app/scripts/profile_vram.py
  docker compose ... start code-index-api

Or copy-paste to run inside a stopped container:
  docker cp scripts/profile_vram.py code-index:/app/scripts/profile_vram.py
  docker start code-index   # without the API server
  docker exec code-index python3 /app/scripts/profile_vram.py
"""
import gc
import json
import os
import sys

os.environ["TOKENIZERS_PARALLELISM"] = "false"

import torch
from sentence_transformers import SentenceTransformer

MODEL_NAME = os.environ.get("EMBEDDING_MODEL", "nomic-ai/CodeRankEmbed")


def free_mb() -> float:
    torch.cuda.synchronize()
    return torch.cuda.mem_get_info()[0] / 1024 ** 2


def peak_mb() -> float:
    return torch.cuda.max_memory_allocated() / 1024 ** 2


def reset():
    torch.cuda.reset_peak_memory_stats()
    torch.cuda.empty_cache()
    gc.collect()


def synthetic_text(n_tokens: int) -> str:
    """Code-like text with ~n_tokens tokens.  1 token ~= 4 ASCII chars."""
    word = "variableName"
    count = max(1, n_tokens * 4 // len(word))
    return " ".join(f"{word}_{i}" for i in range(count))


def profile(model, batch_sizes, token_counts, repeats=3):
    results = []
    total = len(batch_sizes) * len(token_counts)
    done = 0

    for n_tokens in token_counts:
        text = synthetic_text(n_tokens)
        for bs in batch_sizes:
            batch = [text] * bs
            peaks = []
            for _ in range(repeats):
                reset()
                model.encode(
                    batch,
                    show_progress_bar=False,
                    batch_size=bs,
                    normalize_embeddings=False,
                )
                torch.cuda.synchronize()
                peaks.append(peak_mb())
                reset()

            avg_peak = sum(peaks) / len(peaks)
            free = free_mb()
            done += 1
            print(
                f"  [{done:>2}/{total}] tokens={n_tokens:5d}  bs={bs}"
                f"  peak={avg_peak:>7.0f} MB"
                f"  per_item={avg_peak / bs:>7.1f} MB"
                f"  free={free:>7.0f} MB",
                flush=True,
            )
            results.append(
                {
                    "n_tokens": n_tokens,
                    "batch_size": bs,
                    "peak_mb": round(avg_peak, 1),
                    "per_item_mb": round(avg_peak / bs, 1),
                    "free_after_mb": round(free, 1),
                }
            )

    return results


def main():
    if not torch.cuda.is_available():
        sys.exit("ERROR: no CUDA device available")

    total_vram = torch.cuda.get_device_properties(0).total_memory / 1024 ** 2
    print(f"GPU   : {torch.cuda.get_device_name(0)}")
    print(f"VRAM  : {total_vram:.0f} MB total,  {free_mb():.0f} MB free at start")
    print(f"Model : {MODEL_NAME}")
    print("Loading model...", flush=True)

    model = SentenceTransformer(MODEL_NAME, trust_remote_code=True, device="cuda")
    model.encode(["warmup"], show_progress_bar=False)
    reset()
    print(f"Model loaded.  Free VRAM: {free_mb():.0f} MB\n", flush=True)

    batch_sizes = [1, 2, 4, 8]
    # nomic-ai/CodeRankEmbed max_seq_len = 8192
    token_counts = [128, 256, 512, 1024, 2048, 4096, 8192]

    repeats = 3
    print(f"Profiling {len(batch_sizes) * len(token_counts)} combinations "
          f"({repeats} repeats each)...\n")
    results = profile(model, batch_sizes, token_counts, repeats=repeats)

    # ---- summary table ----
    print("\n" + "=" * 68)
    print(f"{'tokens':>7}  {'bs':>3}  {'peak_MB':>8}  {'per_item_MB':>11}  {'free_MB':>8}")
    print("-" * 68)
    for r in results:
        print(
            f"{r['n_tokens']:>7}  {r['batch_size']:>3}"
            f"  {r['peak_mb']:>8.0f}  {r['per_item_mb']:>11.1f}  {r['free_after_mb']:>8.0f}"
        )

    # ---- safe batch sizes for RTX 3090 (leave 4 GB headroom for model + other procs) ----
    headroom_mb = 4096
    available_mb = total_vram - headroom_mb
    print(f"\n--- Safe batch sizes (available={available_mb:.0f} MB, "
          f"headroom={headroom_mb} MB) ---")
    print(f"{'tokens':>7}  {'max_safe_bs':>11}")
    print("-" * 22)
    by_tokens: dict[int, list] = {}
    for r in results:
        by_tokens.setdefault(r["n_tokens"], []).append(r)
    for n_tokens, rows in sorted(by_tokens.items()):
        safe = max(
            (r["batch_size"] for r in rows if r["peak_mb"] <= available_mb),
            default=0,
        )
        print(f"{n_tokens:>7}  {safe:>11}")

    # ---- save JSON ----
    out = "/tmp/vram_profile.json"
    with open(out, "w") as f:
        json.dump(
            {"model": MODEL_NAME, "total_vram_mb": total_vram, "results": results},
            f,
            indent=2,
        )
    print(f"\nRaw data saved to {out}")


if __name__ == "__main__":
    main()