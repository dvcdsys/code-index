#!/usr/bin/env python3
"""
VRAM profiling for the GGUF embedding model.

Measures peak GPU memory for a GGUF model using llama-cpp-python.
Run this with the indexing server STOPPED so measurements are clean.

Usage on the server:
  docker compose -f /path/to/stack/docker-compose.yml stop code-index-api
  docker run --rm --gpus all \
      -e EMBEDDING_MODEL=awhiteside/CodeRankEmbed-Q8_0-GGUF \
      -v cix_cix_data:/data \
      dvcdsys/code-index:test-cu130 \
      python3 /app/scripts/profile_vram.py
  docker compose ... start code-index-api

Override GPU/CPU behaviour with CIX_N_GPU_LAYERS=0 (CPU) or =-1 (all layers on GPU).
"""
import gc
import json
import os
import sys
import time
import subprocess

os.environ["TOKENIZERS_PARALLELISM"] = "false"

from llama_cpp import Llama
from huggingface_hub import hf_hub_download, list_repo_files

MODEL_NAME = os.environ.get("EMBEDDING_MODEL", "awhiteside/CodeRankEmbed-Q8_0-GGUF")

def get_gpu_memory():
    """Returns (used, total) in MB via nvidia-smi."""
    try:
        output = subprocess.check_output(
            ["nvidia-smi", "--query-gpu=memory.used,memory.total", "--format=csv,nounits,noheader"],
            encoding="utf-8"
        )
        used, total = map(int, output.strip().split(","))
        return used, total
    except Exception:
        return 0, 0

def synthetic_text(n_tokens: int) -> str:
    """Code-like text with ~n_tokens tokens."""
    word = "variableName"
    count = max(1, n_tokens * 4 // len(word))
    return " ".join(f"{word}_{i}" for i in range(count))

def main():
    used_start, total_vram = get_gpu_memory()
    if total_vram == 0:
        print("nvidia-smi unavailable — running on CPU or GPU access is missing.")

    print(f"GPU   : NVIDIA (via nvidia-smi)")
    print(f"VRAM  : {total_vram} MB total, {used_start} MB used at start")
    print(f"Model : {MODEL_NAME}")
    print("Loading model...", flush=True)

    model_path = MODEL_NAME
    if "/" in model_path and not os.path.exists(model_path):
        files = list_repo_files(model_path)
        gguf_file = next((f for f in files if f.endswith(".gguf")), None)
        model_path = hf_hub_download(repo_id=model_path, filename=gguf_file)

    n_gpu_layers = int(os.environ.get("CIX_N_GPU_LAYERS", "-1" if total_vram else "0"))
    model = Llama(
        model_path=model_path,
        embedding=True,
        n_ctx=8192,
        n_gpu_layers=n_gpu_layers,
        verbose=False
    )

    used_after_load, _ = get_gpu_memory()
    model_size_mb = used_after_load - used_start
    print(f"Model loaded. VRAM used: {used_after_load} MB (Model ~{model_size_mb} MB)\n", flush=True)

    token_counts = [128, 256, 512, 1024, 2048, 4096, 8192]
    results = []

    print(f"{'tokens':>7}  {'peak_used_MB':>12}  {'delta_MB':>8}")
    print("-" * 35)

    for n_tokens in token_counts:
        text = synthetic_text(n_tokens)
        
        # GGUF usually doesn't show huge VRAM spikes for embeddings like PyTorch does
        # because the context is pre-allocated.
        model.create_embedding(text)
        
        used_now, _ = get_gpu_memory()
        results.append({
            "n_tokens": n_tokens,
            "used_mb": used_now,
            "delta_mb": used_now - used_after_load
        })
        
        print(f"{n_tokens:>7}  {used_now:>12d}  {used_now - used_after_load:>8d}")

    # ---- save JSON ----
    out = "/tmp/vram_profile.json"
    dump_data = {
        "model": MODEL_NAME,
        "total_vram_mb": total_vram,
        "load_vram_mb": used_after_load,
        "results": results
    }
    with open(out, "w") as f:
        json.dump(dump_data, f, indent=2)
    print(f"\nRaw data saved to {out}")

if __name__ == "__main__":
    main()