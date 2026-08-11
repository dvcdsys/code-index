import type { RuntimeConfig } from '@/api/types';
import { Card, CardBody, CardHead } from '@/ui/card';
import { NumberField } from '../components/NumberField';

interface Props {
  config?: RuntimeConfig;
  draftCtx: number;
  draftGpuLayers: number;
  draftThreads: number;
  draftCacheRAM: number;
  onDraftCtx: (n: number) => void;
  onDraftGpuLayers: (n: number) => void;
  onDraftThreads: (n: number) => void;
  onDraftCacheRAM: (n: number) => void;
}

// Flags handed to llama-server on every (re)start. n_gpu_layers accepts -1
// (the Metal/CUDA "all layers" sentinel), so it is deliberately not clamped
// at zero like the rest.
export function RuntimeParamsSection({
  config,
  draftCtx,
  draftGpuLayers,
  draftThreads,
  draftCacheRAM,
  onDraftCtx,
  onDraftGpuLayers,
  onDraftThreads,
  onDraftCacheRAM,
}: Props) {
  const rec = config?.recommended;
  const src = config?.source;
  return (
    <Card>
      <CardHead title="Runtime parameters">
        <span className="ml-2 font-mono text-[11px] font-normal text-dim">restart on change</span>
      </CardHead>
      <CardBody className="grid gap-5 sm:grid-cols-2">
        <NumberField
          field="llama_ctx_size"
          label="Context window"
          hint="Maximum tokens per chunk. Larger costs memory."
          value={draftCtx}
          recommended={rec?.llama_ctx_size}
          source={src?.llama_ctx_size}
          onChange={onDraftCtx}
          min={1}
        />
        <NumberField
          field="llama_n_gpu_layers"
          label="GPU layers"
          hint="-1 offloads everything (Metal/CUDA), 0 is CPU only, >0 is a partial offload."
          value={draftGpuLayers}
          recommended={rec?.llama_n_gpu_layers}
          source={src?.llama_n_gpu_layers}
          onChange={onDraftGpuLayers}
          min={-1}
        />
        <NumberField
          field="llama_n_threads"
          label="CPU threads"
          hint="0 lets llama-server auto-detect via hardware_concurrency."
          value={draftThreads}
          recommended={rec?.llama_n_threads}
          source={src?.llama_n_threads}
          onChange={onDraftThreads}
        />
        <NumberField
          field="llama_cache_ram_mib"
          label="Host prompt cache"
          hint="llama-server's in-RAM prompt cache (--cache-ram), in MiB. Embeddings never reuse prompts, so 0 is right for cix — llama's own 8192 default only inflates RSS until the container hits its memory limit. -1 is unlimited."
          value={draftCacheRAM}
          recommended={rec?.llama_cache_ram_mib}
          source={src?.llama_cache_ram_mib}
          onChange={onDraftCacheRAM}
          min={-1}
        />
      </CardBody>
    </Card>
  );
}
