import { useId } from 'react';
import type { RuntimeConfig } from '@/api/types';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { SourcePill } from '../components/SourcePill';

interface Props {
  config?: RuntimeConfig;
  draftConcurrency: number;
  draftBatch: number;
  draftIndexBatch: number;
  onDraftConcurrency: (n: number) => void;
  onDraftBatch: (n: number) => void;
  onDraftIndexBatch: (n: number) => void;
  // isOllama controls whether the llama-only batch-size field is
  // rendered. Concurrency (the Service-level queue depth) applies to
  // every provider — caps how many parallel /v1/embeddings POSTs go
  // out, which OpenAI / Voyage both honour natively — and is shown
  // regardless.
  isOllama: boolean;
}

// AdvancedSection: throughput-tuning fields most operators won't touch.
// Wrapped in a native <details> so the form stays light by default — Radix
// Collapsible isn't installed and the plain HTML element is good enough.
export function AdvancedSection({
  config,
  draftConcurrency,
  draftBatch,
  draftIndexBatch,
  onDraftConcurrency,
  onDraftBatch,
  onDraftIndexBatch,
  isOllama,
}: Props) {
  const concId = useId();
  const batchId = useId();
  const idxBatchId = useId();
  const rec = config?.recommended;
  const src = config?.source;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Throughput</CardTitle>
        <CardDescription>
          During repo indexing the embedder packs chunks (across files) into
          batched <code>/v1/embeddings</code> POSTs and runs several in
          parallel. Embed batch size sets how many chunks per POST;
          concurrency caps how many POSTs run at once. Both apply to every
          backend and together govern indexing speed. Llama batch (below) is
          sidecar-only.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <details className="group" open>
          <summary className="cursor-pointer text-sm font-medium text-muted-foreground hover:text-foreground">
            Show throughput tunables
          </summary>
          <div className="mt-4 space-y-5">
            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor={concId} className="font-medium">
                  Embedding queue concurrency
                  <span className="ml-2 font-normal text-muted-foreground text-xs">(max_embedding_concurrency)</span>
                </Label>
                <SourcePill source={src?.max_embedding_concurrency} />
              </div>
              <Input
                id={concId}
                type="number"
                min={1}
                value={Number.isFinite(draftConcurrency) ? draftConcurrency : 0}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  onDraftConcurrency(Number.isFinite(n) ? n : 0);
                }}
                className="max-w-xs"
              />
              <p className="text-xs text-muted-foreground">
                Maximum batched <code>/v1/embeddings</code> POSTs in flight
                across the whole server (each POST already carries one
                file's chunks as a batch). 1 = strictly sequential. OpenAI
                and Voyage both accept concurrent requests, but their
                account-level rate limits still apply — start low (e.g. 2)
                and raise it if you don't see 429s. Per-request limits
                (Voyage <code>voyage-code-*</code>: 128 inputs <em>or</em>
                ~100K tokens; OpenAI: 2048 inputs) are split server-side
                under one queue slot, so oversized files are safe.
                Recommended:{' '}
                <code>{rec?.max_embedding_concurrency ?? 1}</code>.
              </p>
            </div>

            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor={idxBatchId} className="font-medium">
                  Embed batch size (indexing)
                  <span className="ml-2 font-normal text-muted-foreground text-xs">(index_embed_batch_chunks)</span>
                </Label>
                <SourcePill source={src?.index_embed_batch_chunks} />
              </div>
              <Input
                id={idxBatchId}
                type="number"
                min={0}
                value={Number.isFinite(draftIndexBatch) ? draftIndexBatch : 0}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  onDraftIndexBatch(Number.isFinite(n) ? n : 0);
                }}
                className="max-w-xs"
              />
              <p className="text-xs text-muted-foreground">
                Max chunks packed into a single embed POST during repo
                indexing — chunks from consecutive small files are merged so
                each POST carries a full payload instead of one tiny file.
                Combined with concurrency above, this is what makes large
                repos index fast. 0 = one POST per file. Recommended:{' '}
                <code>{rec?.index_embed_batch_chunks ?? 64}</code>.
              </p>
            </div>

            {isOllama ? (
              <div className="space-y-1.5">
                <div className="flex items-center justify-between gap-2">
                  <Label htmlFor={batchId} className="font-medium">
                    Llama batch size
                    <span className="ml-2 font-normal text-muted-foreground text-xs">(llama_batch_size, -b)</span>
                  </Label>
                  <SourcePill source={src?.llama_batch_size} />
                </div>
                <Input
                  id={batchId}
                  type="number"
                  min={1}
                  value={Number.isFinite(draftBatch) ? draftBatch : 0}
                  onChange={(e) => {
                    const n = parseInt(e.target.value, 10);
                    onDraftBatch(Number.isFinite(n) ? n : 0);
                  }}
                  className="max-w-xs"
                />
                <p className="text-xs text-muted-foreground">
                  Logical batch passed to llama-server (-b). 0 = match context window.
                  Recommended: <code>{rec?.llama_batch_size ?? 'ctx'}</code>.
                </p>
              </div>
            ) : null}
          </div>
        </details>
      </CardContent>
    </Card>
  );
}
