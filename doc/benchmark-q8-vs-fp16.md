# CodeRankEmbed GGUF Quantization Benchmark

**Date:** 2026-04-23
**Hardware:** macOS, Apple Silicon, Metal backend (llama-cpp-python, `n_gpu_layers=-1`)
**fp16 reference:** `nomic-ai/CodeRankEmbed` via sentence-transformers (MPS device)
**GGUF source:** `limcheekin/CodeRankEmbed-GGUF` (F16, Q8_0, Q5_K_M, Q4_K_M)
**Corpus:** `/Users/dvcdsys/Cursor/claude-code-index` — 218 code chunks, 20 queries, k=10

## Acceptance thresholds

| Metric | Threshold |
|---|---:|
| Jaccard@10 | ≥ 0.70 |
| Recall@10  | ≥ 0.90 |
| Kendall τ  | ≥ 0.50 |

## Main results table

| Quant | File size | Load time | Jaccard@10 | Recall@10 | Kendall τ | Pass? |
|---|---:|---:|---:|---:|---:|:---:|
| fp16 ref (sentence-transformers) | ~522 MB | 6.2s | — | — | — | reference |
| F16 GGUF | 261 MB | ~8.6s | 0.894 | 0.940 | 0.879 | PASS |
| **Q8_0** (current default) | **139 MB** | ~1.7s | **0.894** | **0.940** | **0.861** | **PASS** |
| Q5_K_M | 98 MB | ~9.2s | 0.815 | 0.895 | 0.786 | FAIL (Recall) |
| Q4_K_M | 86 MB | ~6.3s | 0.787 | 0.875 | 0.760 | FAIL (Recall) |

> Load times include GGUF download check + model init. Embed times (218 chunks + 20 queries, one-by-one):
> F16 ≈ 4.2s, Q8_0 ≈ 4.3s, Q5_K_M ≈ 4.8s, Q4_K_M ≈ 4.6s.
> fp16 reference (MPS, batch_size=8): 11.5s — all GGUFs are ~2.4–2.7× faster on this corpus.

## Key observations

1. **F16 GGUF ≈ Q8_0 in quality** — both score Jaccard 0.894, Recall 0.940. F16 has marginally better Kendall τ (0.879 vs 0.861) but uses 2× the disk (261 MB vs 139 MB). No practical reason to prefer F16 GGUF over Q8_0.

2. **Q8_0 is the sweet spot** — matches F16 quality at exactly half the size. All three acceptance criteria pass with substantial headroom (Recall 0.940 vs threshold 0.90; τ 0.861 vs threshold 0.50).

3. **Q5_K_M fails narrowly** — Recall 0.895 misses the 0.90 threshold by 0.005. Jaccard and τ both pass. On a larger or more diverse corpus this marginal failure might shrink or grow. It saves only 41 MB vs Q8_0 (98 MB vs 139 MB) — not worth the quality regression.

4. **Q4_K_M fails clearly** — Recall 0.875 (threshold 0.90). Both Jaccard (0.787) and Recall are notably below Q8_0. 4-bit quantization is too aggressive for a 137M embedding model where every weight matters.

## Conclusion

**Keep Q8_0.** It is the correct default:
- Meets all three acceptance thresholds with margin (Jaccard +0.19, Recall +0.04, τ +0.36 above thresholds).
- 2.6× faster than the fp16 sentence-transformers reference on Apple Silicon.
- Identical retrieval quality to F16 GGUF at half the file size.
- Q5_K_M and Q4_K_M both fail Recall@10 and are not recommended for production.

The original migration claim — "negligible quality loss" — is validated: Q8_0 GGUF has near-identical top-k retrieval to the fp16 PyTorch reference.

## Total disk footprint of downloaded GGUF files

| File | Size |
|---|---:|
| `awhiteside/CodeRankEmbed-Q8_0-GGUF` (pre-existing) | 139 MB |
| `limcheekin/CodeRankEmbed-GGUF` — Q8_0 | 139 MB |
| `limcheekin/CodeRankEmbed-GGUF` — F16 | 261 MB |
| `limcheekin/CodeRankEmbed-GGUF` — Q5_K_M | 98 MB |
| `limcheekin/CodeRankEmbed-GGUF` — Q4_K_M | 86 MB |
| `nomic-ai/CodeRankEmbed` fp16 reference | ~522 MB |
| **Total new downloads** | **~1.1 GB** |

To clean up the limcheekin and nomic-ai downloads (keep awhiteside Q8_0 which is already in use):
```bash
rm -rf ~/.cache/huggingface/hub/models--limcheekin--CodeRankEmbed-GGUF
rm -rf ~/.cache/huggingface/hub/models--nomic-ai--CodeRankEmbed
```

## Per-query detail

See supporting files:
- `doc/benchmark-q8_0.md` — F16 ref vs Q8_0
- `doc/benchmark-q5_k_m.md` — F16 ref vs Q5_K_M
- `doc/benchmark-q4_k_m.md` — F16 ref vs Q4_K_M
- `doc/benchmark-f16.md` — F16 ref vs F16 GGUF
- `doc/benchmark-data/` — raw top-k JSON per quant (`benchmark-*.json`) and
  `fp16-cache.json` (reusable reference cache; safe to delete after review)
