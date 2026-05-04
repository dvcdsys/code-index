import { useId } from 'react';
import type { RuntimeConfig } from '@/api/types';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { SourcePill } from '../components/SourcePill';

interface NumberFieldProps {
  field: string;
  label: string;
  hint: string;
  value: number;
  recommended?: number;
  source?: string;
  onChange: (next: number) => void;
  min?: number;
}

function NumberField({ field, label, hint, value, recommended, source, onChange, min = 0 }: NumberFieldProps) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label htmlFor={id} className="font-medium">
          {label}
          <span className="ml-2 font-normal text-muted-foreground text-xs">({field})</span>
        </Label>
        <SourcePill source={source} />
      </div>
      <Input
        id={id}
        type="number"
        min={min}
        value={Number.isFinite(value) ? value : 0}
        onChange={(e) => {
          const n = parseInt(e.target.value, 10);
          onChange(Number.isFinite(n) ? n : 0);
        }}
        className="max-w-xs"
      />
      <p className="text-xs text-muted-foreground">
        {hint}
        {recommended !== undefined ? <> Recommended: <code>{recommended}</code>.</> : null}
      </p>
    </div>
  );
}

interface Props {
  config?: RuntimeConfig;
  draftCtx: number;
  draftGpuLayers: number;
  draftThreads: number;
  onDraftCtx: (n: number) => void;
  onDraftGpuLayers: (n: number) => void;
  onDraftThreads: (n: number) => void;
}

// RuntimeParamsSection: ctx, n_gpu_layers, n_threads form. n_gpu_layers
// allows -1 (Metal/CUDA all-layers sentinel) so we deliberately do NOT
// clamp to >= 0 in the input.
export function RuntimeParamsSection({
  config,
  draftCtx,
  draftGpuLayers,
  draftThreads,
  onDraftCtx,
  onDraftGpuLayers,
  onDraftThreads,
}: Props) {
  const rec = config?.recommended;
  const src = config?.source;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Runtime parameters</CardTitle>
        <CardDescription>
          Tunables passed to llama-server on every (re)start. Leaving a field
          at zero falls back to env / recommended on the next save.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <NumberField
          field="llama_ctx_size"
          label="Context window"
          hint="Maximum tokens per chunk. Larger values use more memory."
          value={draftCtx}
          recommended={rec?.llama_ctx_size}
          source={src?.llama_ctx_size}
          onChange={onDraftCtx}
          min={1}
        />
        <NumberField
          field="llama_n_gpu_layers"
          label="GPU layers"
          hint="-1 = all (Metal/CUDA), 0 = CPU only, >0 = partial offload."
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
      </CardContent>
    </Card>
  );
}
