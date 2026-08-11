import type { RuntimeConfig } from '@/api/types';
import { Callout } from '@/ui/alert';
import { Card, CardBody, CardHead } from '@/ui/card';
import { Chip } from '@/ui/code';
import { NumberField } from '../components/NumberField';

interface Props {
  config?: RuntimeConfig;
  draftConcurrency: number;
  draftBatch: number;
  draftIndexBatch: number;
  draftChunkConc: number;
  onDraftConcurrency: (n: number) => void;
  onDraftBatch: (n: number) => void;
  onDraftIndexBatch: (n: number) => void;
  onDraftChunkConc: (n: number) => void;
  // The llama-only batch-size field is gated on this. Queue concurrency is
  // the Service-level cap on parallel /v1/embeddings POSTs and every provider
  // honours it, so that one is always shown.
  isOllama: boolean;
}

// Throughput. The long explanation that used to float above the fields is a
// callout now — prose that matters gets a border, prose that doesn't is gone.
export function AdvancedSection({
  config,
  draftConcurrency,
  draftBatch,
  draftIndexBatch,
  draftChunkConc,
  onDraftConcurrency,
  onDraftBatch,
  onDraftIndexBatch,
  onDraftChunkConc,
  isOllama,
}: Props) {
  const rec = config?.recommended;
  const src = config?.source;

  return (
    <Card>
      <CardHead title="Throughput" />
      <CardBody className="flex flex-col gap-5">
        <Callout>
          <b>How indexing throughput is shaped</b>
          <p>
            The embedder packs chunks from consecutive files into batched{' '}
            <Chip>/v1/embeddings</Chip> POSTs and runs several in parallel. Batch size sets how
            many chunks ride in one POST; concurrency caps how many POSTs are in flight. Both
            apply to every provider.
          </p>
        </Callout>

        <div className="grid gap-5 sm:grid-cols-2">
          <NumberField
            field="max_embedding_concurrency"
            label="Queue concurrency"
            hint="Batched POSTs in flight across the whole server. 1 is strictly sequential. OpenAI and Voyage accept concurrency but enforce account rate limits — start at 2 and raise it until you see 429s. Per-request caps are split server-side under one slot, so oversized files are safe."
            value={draftConcurrency}
            recommended={rec?.max_embedding_concurrency ?? 1}
            source={src?.max_embedding_concurrency}
            onChange={onDraftConcurrency}
            min={1}
          />
          <NumberField
            field="index_embed_batch_chunks"
            label="Embed batch size"
            hint="Chunks packed into a single POST during a repo index — small files merge so each POST carries a full payload. 0 means one POST per file."
            value={draftIndexBatch}
            recommended={rec?.index_embed_batch_chunks ?? 64}
            source={src?.index_embed_batch_chunks}
            onChange={onDraftIndexBatch}
          />
          <NumberField
            field="chunk_max_concurrent"
            label="Chunker concurrency"
            hint="Tree-sitter (wasm) parser instances running at once — the chunker's own concurrency, independent of embedding. Each holds ~69 MiB, so this bounds peak chunker memory. Applies live, no restart."
            value={draftChunkConc}
            recommended={rec?.chunk_max_concurrent ?? 3}
            source={src?.chunk_max_concurrent}
            onChange={onDraftChunkConc}
          />
          {isOllama ? (
            <NumberField
              field="llama_batch_size"
              label="Llama batch"
              hint="Logical batch passed to llama-server (-b). 0 matches the context window."
              value={draftBatch}
              recommended={rec?.llama_batch_size ?? 'ctx'}
              source={src?.llama_batch_size}
              onChange={onDraftBatch}
              min={1}
            />
          ) : null}
        </div>
      </CardBody>
    </Card>
  );
}
