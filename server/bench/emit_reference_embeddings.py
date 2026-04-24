#!/usr/bin/env python3
"""Emit reference embeddings for Bench 3 (embed_parity).

Runs the exact same model + query prefix logic as api/app/services/embeddings.py
and writes one JSON file per phrase plus a summary. Must be run with the api/
venv active (llama-cpp-python + huggingface_hub installed).

Usage:
    cd server/bench
    python emit_reference_embeddings.py

Output:
    results/reference_embeddings.json  — {"model": ..., "dim": ..., "items": [{"phrase", "is_query", "vector": [...]}]}
    results/reference_gguf_path.txt    — absolute path of the GGUF file used

The GGUF path in the second file is what Go side must load to get parity.
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

MODEL = os.environ.get("CIX_EMBEDDING_MODEL", "awhiteside/CodeRankEmbed-Q8_0-GGUF")
QUERY_PREFIX = "Represent this query for searching relevant code: "

# (phrase, is_query) — 10 items, mixing code + natural language
PHRASES: list[tuple[str, bool]] = [
    ("func Greet(name string) string { return \"Hello, \" + name }", False),
    ("def greet(name): return f'Hello, {name}'", False),
    ("class Repository:\n    def find(self, name): ...", False),
    ("// Parse YAML config and return structured settings", False),
    ("SELECT id, name FROM users WHERE age > 18", False),
    ("how to parse yaml file in go", True),
    ("find user by name in database", True),
    ("implement a binary search tree", True),
    ("The quick brown fox jumps over the lazy dog.", False),
    ("authentication middleware for http requests", True),
]


def main() -> int:
    out_dir = Path(__file__).parent / "results"
    out_dir.mkdir(parents=True, exist_ok=True)

    try:
        from huggingface_hub import hf_hub_download, list_repo_files
        from llama_cpp import Llama
    except ImportError as e:
        print(f"ERROR: missing dependency: {e}\n"
              "Activate the api/ venv first: source api/.venv/bin/activate",
              file=sys.stderr)
        return 2

    # Resolve model path (same logic as EmbeddingService._load_model_sync)
    model_path = MODEL
    if "/" in model_path and not Path(model_path).exists():
        print(f"Downloading GGUF from HF: {model_path}", file=sys.stderr)
        files = list_repo_files(model_path)
        gguf_file = next((f for f in files if f.endswith(".gguf")), None)
        if not gguf_file:
            print(f"ERROR: no .gguf file in repo {model_path}", file=sys.stderr)
            return 3
        model_path = hf_hub_download(repo_id=model_path, filename=gguf_file)

    print(f"Loading {model_path}", file=sys.stderr)
    llm = Llama(
        model_path=model_path,
        embedding=True,
        n_ctx=2048 + 128,
        n_threads=int(os.environ.get("OMP_NUM_THREADS", "4")),
        n_gpu_layers=-1 if sys.platform == "darwin" else 0,
        verbose=False,
    )

    dim = int(llm.n_embd())
    items = []
    for phrase, is_query in PHRASES:
        text = (QUERY_PREFIX + phrase) if is_query else phrase
        res = llm.create_embedding(text)
        vec = res["data"][0]["embedding"]
        items.append({
            "phrase": phrase,
            "is_query": is_query,
            "text_sent_to_model": text,
            "vector": vec,
        })
        print(f"  [{'Q' if is_query else ' '}] {phrase[:60]}...", file=sys.stderr)

    output = {
        "model": MODEL,
        "gguf_path": model_path,
        "dim": dim,
        "query_prefix": QUERY_PREFIX,
        "items": items,
    }
    (out_dir / "reference_embeddings.json").write_text(json.dumps(output, indent=2))
    (out_dir / "reference_gguf_path.txt").write_text(model_path + "\n")
    print(f"Wrote {out_dir / 'reference_embeddings.json'}", file=sys.stderr)
    print(f"GGUF path: {model_path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
