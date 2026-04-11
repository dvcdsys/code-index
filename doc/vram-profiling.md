# VRAM Profiling — nomic-ai/CodeRankEmbed on RTX 3090

## Goal

Determine the relationship between chunk size (sequence length in tokens) and
peak GPU memory usage so the server can automatically choose a batch size that
keeps total VRAM consumption under **5 GB** — the budget required to coexist
with other processes (e.g. Ollama ~5 GB) on a 24 GB RTX 3090.

---

## Methodology

### Environment

| Item | Value |
|------|-------|
| GPU | NVIDIA GeForce RTX 3090 (24 126 MB) |
| Image | `dvcdsys/code-index:latest-cu130` (CUDA 12.6 / PyTorch cu130) |
| Model | `nomic-ai/CodeRankEmbed` |
| Model VRAM at idle | ~644 MB (18 302 MB free before load → 17 658 MB free after) |

### Pre-conditions

The production `code-index` container was **stopped** before running the
profiler.  A live server holds ~13 GB in PyTorch's memory pool even when idle,
which would corrupt the measurements.  A fresh one-off container was launched
with `--gpus all` against the same `cix_cix_data` volume.

### Measurement procedure (`scripts/profile_vram.py`)

For every `(batch_size, seq_len)` combination:

1. `torch.cuda.reset_peak_memory_stats()` + `torch.cuda.empty_cache()` +
   `gc.collect()` — start from a clean baseline.
2. `model.encode(batch, batch_size=bs, normalize_embeddings=False)`.
3. `torch.cuda.synchronize()` → read `torch.cuda.max_memory_allocated()` —
   this is the **peak allocation** during that call.
4. Repeat 3 times and average.

**Synthetic text**: `"variableName_0 variableName_1 …"` at roughly the target
token count (1 token ≈ 4 ASCII chars).

Parameters tested:

| Parameter | Values |
|-----------|--------|
| `batch_size` | 1, 2, 4, 8 |
| `token_count` | 128, 256, 512, 1 024, 2 048, 4 096, 8 192 |
| Repeats per combo | 3 |

---

## Results

VRAM free before model load: **18 302 MB**
VRAM free after model load: **17 658 MB** → model uses **~644 MB**

### Raw measurements

`peak MB` = peak GPU memory allocated during `model.encode()` for the whole
batch (includes model weights + activations + KV cache for that call).
`per-item MB` = peak MB divided by batch size — useful for comparing efficiency.

| tokens | bs | peak MB | per-item MB |
|-------:|---:|--------:|------------:|
|    128 |  1 |     541 |       540.8 |
|    128 |  2 |     551 |       275.4 |
|    128 |  4 |     571 |       142.7 |
|    128 |  8 |     611 |        76.3 |
|    256 |  1 |     555 |       555.3 |
|    256 |  2 |     571 |       285.7 |
|    256 |  4 |     611 |       152.9 |
|    256 |  8 |     692 |        86.5 |
|    512 |  1 |     590 |       590.3 |
|    512 |  2 |     646 |       322.8 |
|    512 |  4 |     760 |       190.1 |
|    512 |  8 |     985 |       123.1 |
|  1 024 |  1 |     734 |       734.3 |
|  1 024 |  2 |     932 |       466.0 |
|  1 024 |  4 |   1 330 |       332.6 |
|  1 024 |  8 |   2 127 |       265.8 |
|  2 048 |  1 |   1 422 |     1 422.4 |
|  2 048 |  2 |   2 308 |     1 153.9 |
|  2 048 |  4 |   4 077 |     1 019.3 |
|  2 048 |  8 |   7 607 |       950.9 |
|  4 096 |  1 |   4 402 |     4 401.8 |
|  4 096 |  2 |   8 257 |     4 128.7 |
|  4 096 |  4 |   — OOM — | — |

Combinations `(4 096, bs≥4)` and all `8 192`-token cases triggered
`torch.OutOfMemoryError` — CUDA allocator fragmentation prevented the
~7–16 GB contiguous allocations required.

### Scaling observations

- **Short sequences (≤ 512 tokens)**: peak scales near-linearly with batch
  size. Per-item cost drops from ~590 MB (bs=1) to ~123 MB (bs=8) — batching
  is efficient here.
- **Long sequences (≥ 2 048 tokens)**: quadratic attention dominates. Peak
  grows super-linearly; for 4 096 tokens bs=2 already needs 8+ GB.
- **4 096 → 8 192 tokens**: based on the doubling trend (~3× per 2× seq len
  at long sequences), bs=1 at 8 192 tokens would require ~12–16 GB.

---

## Safe batch sizes (5 GB budget)

Target: **5 120 MB total** (model ~644 MB + embedding peak ≤ ~4 476 MB).

| Estimated tokens | Max safe `batch_size` | Peak VRAM |
|-----------------:|----------------------:|----------:|
|         ≤ 1 024  |                     8 |  ≤ 2 127 MB |
|         ≤ 2 048  |                     4 |  ≤ 4 077 MB |
|         ≤ 4 096  |                     1 |  ≤ 4 402 MB |
|         > 4 096  |                     1 |  likely OOM |

Token count is estimated at runtime: `avg_char_length / 4`
(1 token ≈ 4 ASCII characters — conservative for code).

---

## Implementation

The lookup table is encoded in `api/app/services/embeddings.py` as
`_BATCH_LIMITS`. The function `_safe_batch_size(avg_chars)` selects the
largest safe batch size at runtime:

```python
_BATCH_LIMITS: list[tuple[int, int]] = [
    (256,  8),   # peak ≤  692 MB
    (512,  8),   # peak ≤  985 MB
    (1024, 8),   # peak ≤ 2127 MB
    (2048, 4),   # peak ≤ 4077 MB
    (4096, 1),   # peak ≤ 4402 MB
    (8192, 1),   # likely OOM even at bs=1
]

def _safe_batch_size(avg_chars: float) -> int:
    est_tokens = int(avg_chars / 4)
    for max_tokens, max_bs in _BATCH_LIMITS:
        if est_tokens <= max_tokens:
            return max_bs
    return 1
```

`_embed_locked` computes the average character length of the incoming batch
once and calls `_safe_batch_size` to determine the sub-batch size for that
request.

---

## Re-running the profiler

If the model or hardware changes, stop the production container and run:

```bash
docker run --rm --gpus all \
    -e EMBEDDING_MODEL=nomic-ai/CodeRankEmbed \
    -v cix_cix_data:/data \
    dvcdsys/code-index:latest-cu130 \
    python3 /app/scripts/profile_vram.py
```

Results are printed to stdout and saved to `/tmp/vram_profile.json` inside the
container. Update `_BATCH_LIMITS` in `embeddings.py` based on the new numbers.