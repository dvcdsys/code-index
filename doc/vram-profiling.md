# VRAM Profiling — GGUF Embedding Model

## Overview

cix-server runs the embedding model out of process: a `llama-server` sidecar
(from llama.cpp) loads the GGUF weights and the Go server talks to it over a
Unix socket (or TCP). VRAM accounting therefore belongs to the sidecar, not the
cix-server binary.

- Weights are loaded once, in a quantised format (Q8_0 ≈ 8-bit), so static
  weight footprint is much smaller than the fp16 Torch equivalent.
- The KV / embedding context (`CIX_LLAMA_CTX`, default 2048) is pre-allocated
  up front. Peak VRAM is therefore near-constant across sequence lengths —
  there is no quadratic attention spike per request.
- GPU offload is controlled by `CIX_N_GPU_LAYERS` (`99` = all layers, `0` =
  CPU only). The same env var works for macOS Metal and Linux CUDA — the
  bundled `llama-server` build picks the right backend at startup.

## Expected baseline

The numbers below are the *design targets* for the production box
(RTX 3090, CUDA, `awhiteside/CodeRankEmbed-Q8_0-GGUF`). They need to be
re-measured with `nvidia-smi` against a running cix-server CUDA container —
this document will be updated with the real figures once captured.

| Item | Expected value |
|------|---------------|
| Model | `awhiteside/CodeRankEmbed-Q8_0-GGUF` |
| Quantisation | Q8_0 (8-bit) |
| On-disk size | ~145 MB |
| Weights in VRAM | ~200-250 MB |
| Context (`CIX_LLAMA_CTX=2048`) | pre-allocated, ~200-400 MB |
| Total idle VRAM | **~0.5-0.7 GB** |

For comparison, the previous PyTorch + `nomic-ai/CodeRankEmbed` (fp16) stack
sat at roughly **4 GB idle** with additional spikes during inference.

## Batch size and sequence length

`llama-server` exposes the OpenAI-compatible `/v1/embeddings` endpoint; the Go
server batches according to `CIX_MAX_EMBEDDING_CONCURRENCY` and forwards each
batch in one HTTP request. Peak VRAM depends on `CIX_LLAMA_CTX`, not on the
batch size, so OOM errors are rare as long as the context fits.

## Measuring on a running container

There is no in-tree profiler script — the simplest approach is `nvidia-smi`
against the live container:

```bash
# Continuous read while indexing exercises the sidecar
docker exec -it <cix-cuda-container> nvidia-smi --query-gpu=memory.used,memory.free \
    --format=csv -l 2
```

Tune via env:

- `CIX_N_GPU_LAYERS=0` — force CPU mode (no VRAM).
- `CIX_N_GPU_LAYERS=99` — force full GPU offload (default in the CUDA image).
- `CIX_LLAMA_CTX=<n>` — pre-allocated context; bigger ctx → bigger idle VRAM.

## Observations (expected, to be validated)

1. **Deterministic footprint** — memory usage is almost entirely defined at
   load time. Per-request delta should be near zero.
2. **Long sequences fit comfortably** — 8192-token inputs stay within the
   pre-allocated context; no growth beyond that.
3. **Multi-tenancy friendly** — a sub-1 GB idle footprint leaves >20 GB free
   on the 3090 for other models (DeepSeek, Granite LLMs) alongside the
   index.

Once `nvidia-smi` numbers have been captured on the production server this
section should be replaced with the actual measured deltas per token-count row.
