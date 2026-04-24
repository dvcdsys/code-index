# VRAM Profiling — GGUF Embedding Model

## Overview

Switching from PyTorch / `sentence-transformers` to `llama-cpp-python` with GGUF
weights changes memory management:

- Weights are loaded once, in a quantised format (Q8_0 ≈ 8-bit), so static
  weight footprint is much smaller than the fp16 Torch equivalent.
- The KV / embedding context (`n_ctx`) is pre-allocated up front. Peak VRAM is
  therefore near-constant across sequence lengths — there is no quadratic
  attention spike per request.
- GPU offload is controlled by `n_gpu_layers` (`-1` = all layers). On macOS
  Metal and Linux CUDA the same flag works transparently once the matching
  wheel is installed.

## Expected baseline

The numbers below are the *design targets* for the production box
(RTX 3090, CUDA, `awhiteside/CodeRankEmbed-Q8_0-GGUF`). They need to be
remeasured with `scripts/profile_vram.py` after deploying the new image —
this document will be updated with the real figures once captured.

| Item | Expected value |
|------|---------------|
| Model | `awhiteside/CodeRankEmbed-Q8_0-GGUF` |
| Quantisation | Q8_0 (8-bit) |
| On-disk size | ~145 MB |
| Weights in VRAM | ~200-250 MB |
| Context (`n_ctx=8192`) | pre-allocated, ~200-400 MB |
| Total idle VRAM | **~0.5-0.7 GB** |

For comparison, the previous PyTorch + `nomic-ai/CodeRankEmbed` (fp16) stack
sat at roughly **4 GB idle** with additional spikes during inference.

## Batch size and sequence length

`llama-cpp-python` accepts a `List[str]` in `create_embedding(...)` and returns
one embedding per input. Peak VRAM depends on `n_ctx`, not on the batch size,
so OOM errors are rare as long as the context fits.

The API server passes full sub-batches (`settings.max_embedding_concurrency`
items) to a single `create_embedding` call — see
`api/app/services/embeddings.py::_embed_locked`.

## Running the profiler

`scripts/profile_vram.py` loads the model in the same way the API does and
probes `nvidia-smi` after each synthetic embedding call to capture peak VRAM.

```bash
# stop the running API so we get clean readings
docker compose -f /path/to/stack/docker-compose.yml stop code-index-api

docker run --rm --gpus all \
    -e EMBEDDING_MODEL=awhiteside/CodeRankEmbed-Q8_0-GGUF \
    -v cix_cix_data:/data \
    dvcdsys/code-index:latest-cu130 \
    python3 /app/scripts/profile_vram.py

docker compose -f /path/to/stack/docker-compose.yml start code-index-api
```

Overrides:

- `CIX_N_GPU_LAYERS=0` — force CPU mode.
- `CIX_N_GPU_LAYERS=-1` — force full GPU offload (default when `nvidia-smi`
  or Metal is detected).

The script writes raw results to `/tmp/vram_profile.json` — copy them out of
the container if you want to drop them in this document.

## Observations (expected, to be validated)

1. **Deterministic footprint** — memory usage is almost entirely defined at
   load time. Per-request delta should be near zero.
2. **Long sequences fit comfortably** — 8192-token inputs stay within the
   pre-allocated context; no growth beyond that.
3. **Multi-tenancy friendly** — a sub-1 GB idle footprint leaves >20 GB free
   on the 3090 for other models (DeepSeek, Granite LLMs) alongside the
   index.

Once `profile_vram.py` has been run on the production server this section
should be replaced with the actual measured deltas per token-count row.
