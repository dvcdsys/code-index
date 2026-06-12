import { useEffect, useMemo, useState } from 'react';
import { AlertCircle, Loader2, Save } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { RuntimeConfig, RuntimeConfigUpdate } from '@/api/types';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import { useServerStatus } from '@/lib/useServerStatus';
import {
  useRestartSidecar,
  useRuntimeConfig,
  useSidecarStatus,
  useUpdateRuntimeConfig,
} from './hooks';
import { EmbeddingModelSection } from './sections/EmbeddingModelSection';
import { RuntimeParamsSection } from './sections/RuntimeParamsSection';
import { SidecarSection } from './sections/SidecarSection';
import { AdvancedSection } from './sections/AdvancedSection';
import { EmbeddingProviderSection } from './sections/EmbeddingProviderSection';
import { SaveAndRestartDialog } from './components/SaveAndRestartDialog';

interface Draft {
  embedding_model: string;
  llama_ctx_size: number;
  llama_n_gpu_layers: number;
  llama_n_threads: number;
  max_embedding_concurrency: number;
  llama_batch_size: number;
  index_embed_batch_chunks: number;
  chunk_max_concurrent: number;
  llama_cache_ram_mib: number;
}

function configToDraft(c: RuntimeConfig): Draft {
  return {
    embedding_model: c.embedding_model,
    llama_ctx_size: c.llama_ctx_size,
    llama_n_gpu_layers: c.llama_n_gpu_layers,
    llama_n_threads: c.llama_n_threads,
    max_embedding_concurrency: c.max_embedding_concurrency,
    llama_batch_size: c.llama_batch_size,
    index_embed_batch_chunks: c.index_embed_batch_chunks,
    chunk_max_concurrent: c.chunk_max_concurrent,
    llama_cache_ram_mib: c.llama_cache_ram_mib,
  };
}

// diffPatch produces (a) the partial PUT body containing only changed
// fields and (b) the human-readable changes list the confirm dialog renders.
function diffPatch(c: RuntimeConfig, d: Draft): { patch: RuntimeConfigUpdate; changes: Array<{ field: string; from: string; to: string }> } {
  const patch: RuntimeConfigUpdate = {};
  const changes: Array<{ field: string; from: string; to: string }> = [];
  if (d.embedding_model !== c.embedding_model) {
    patch.embedding_model = d.embedding_model;
    changes.push({ field: 'embedding_model', from: c.embedding_model, to: d.embedding_model });
  }
  for (const k of [
    'llama_ctx_size',
    'llama_n_gpu_layers',
    'llama_n_threads',
    'max_embedding_concurrency',
    'llama_batch_size',
    'index_embed_batch_chunks',
    'chunk_max_concurrent',
    'llama_cache_ram_mib',
  ] as const) {
    if (d[k] !== c[k]) {
      patch[k] = d[k];
      changes.push({ field: k, from: String(c[k]), to: String(d[k]) });
    }
  }
  return { patch, changes };
}

export default function ServerPage() {
  const cfg = useRuntimeConfig();
  const status = useSidecarStatus();
  const update = useUpdateRuntimeConfig();
  const restart = useRestartSidecar();
  // /status is shared with the footer (already polled every 30s) and
  // its embedding_provider field reflects the LIVE active provider —
  // the right signal for "should we show ollama sections?". We default
  // to true while it loads so the page doesn't flash empty between
  // mount and the first /status response.
  const serverStatus = useServerStatus();
  const activeKind = serverStatus.data?.embedding_provider ?? 'ollama';
  const showOllamaSections = activeKind === 'ollama';

  const [draft, setDraft] = useState<Draft | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);

  // Initialise / reset draft whenever the server-side config changes from
  // under us (initial fetch, optimistic refresh after save).
  useEffect(() => {
    if (cfg.data) setDraft(configToDraft(cfg.data));
  }, [cfg.data]);

  const dirty = useMemo(() => {
    if (!cfg.data || !draft) return false;
    return diffPatch(cfg.data, draft).changes.length > 0;
  }, [cfg.data, draft]);

  if (cfg.isLoading || !draft) {
    return (
      <div className="space-y-6">
        <header>
          <h1 className="text-2xl font-semibold tracking-tight">Server</h1>
          <p className="text-sm text-muted-foreground">Embedding model, indexing parameters, sidecar lifecycle.</p>
        </header>
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (cfg.error || !cfg.data) {
    return (
      <Alert variant="destructive">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>Could not load runtime config</AlertTitle>
        <AlertDescription>{cfg.error instanceof ApiError ? cfg.error.detail : String(cfg.error)}</AlertDescription>
      </Alert>
    );
  }

  const disabled = status.data?.state === 'disabled';
  const { changes } = diffPatch(cfg.data, draft);

  async function onConfirm() {
    if (!cfg.data || !draft) return;
    const { patch } = diffPatch(cfg.data, draft);
    try {
      // Step 1 — write overrides to DB. The mutation also refreshes the
      // cache so the form's "DB" pills appear before the restart fires.
      if (Object.keys(patch).length > 0) {
        await update.mutateAsync(patch);
      }
      // Step 2 — kick a sidecar restart so the new model / flags load.
      await restart.mutateAsync();
      setConfirmOpen(false);
      toast.success('Configuration saved', {
        description: 'Sidecar is restarting — watch the Sidecar card for status.',
      });
    } catch (e) {
      const detail = e instanceof ApiError ? e.detail : String(e);
      toast.error('Save & Restart failed', { description: detail });
    }
  }

  const isPending = update.isPending || restart.isPending;

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Server</h1>
          <p className="text-sm text-muted-foreground">
            {showOllamaSections
              ? 'Embedding provider + model, indexing parameters, sidecar lifecycle, throughput. Saved overrides land in the database and are reapplied on the next sidecar restart — env vars stay as bootstrap defaults.'
              : 'Embedding provider + concurrency. For remote providers (OpenAI-compatible, Voyage) the per-provider form above is the main edit surface; this page also exposes the server-wide concurrency cap that all providers honour.'}
          </p>
        </div>
        <Button
          onClick={() => setConfirmOpen(true)}
          disabled={!dirty || isPending || disabled}
        >
          {isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : <Save className="mr-1 h-4 w-4" />}
          {showOllamaSections ? 'Save & Restart' : 'Save'}
        </Button>
      </header>

      {disabled ? (
        <Alert>
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Embeddings disabled at boot</AlertTitle>
          <AlertDescription>
            The server was started with <code>CIX_EMBEDDINGS_ENABLED=false</code>.
            Restart the server with the env var set to <code>true</code> to
            enable runtime config + the sidecar.
          </AlertDescription>
        </Alert>
      ) : null}

      <EmbeddingProviderSection />

      {/*
        Ollama-specific cards — rendered only when the active provider
        is ollama. For openai/voyage these sections do not apply:
        there's no GGUF, no llama-server child to restart, no GPU
        layers / threads, no batch size knob. The provider form above
        is the only edit surface in that case.

        Concurrency lives inside AdvancedSection together with the
        ollama-only batch size — for v1 we hide the whole card on
        remote providers. A follow-up may split concurrency into a
        provider-agnostic card if operators ask for it.
      */}
      {showOllamaSections ? (
        <>
          <EmbeddingModelSection
            config={cfg.data}
            draftModel={draft.embedding_model}
            onDraftChange={(v) => setDraft({ ...draft, embedding_model: v })}
          />

          <RuntimeParamsSection
            config={cfg.data}
            draftCtx={draft.llama_ctx_size}
            draftGpuLayers={draft.llama_n_gpu_layers}
            draftThreads={draft.llama_n_threads}
            draftCacheRAM={draft.llama_cache_ram_mib}
            onDraftCtx={(n) => setDraft({ ...draft, llama_ctx_size: n })}
            onDraftGpuLayers={(n) => setDraft({ ...draft, llama_n_gpu_layers: n })}
            onDraftThreads={(n) => setDraft({ ...draft, llama_n_threads: n })}
            onDraftCacheRAM={(n) => setDraft({ ...draft, llama_cache_ram_mib: n })}
          />

          <SidecarSection />
        </>
      ) : null}

      {/*
        Throughput / concurrency — always visible. The queue concurrency
        is the Service-level cap on parallel /v1/embeddings POSTs and
        applies to every provider (ollama, openai, voyage all accept
        concurrent requests). The llama batch field inside the card
        is gated on isOllama.
      */}
      <AdvancedSection
        config={cfg.data}
        draftConcurrency={draft.max_embedding_concurrency}
        draftBatch={draft.llama_batch_size}
        draftIndexBatch={draft.index_embed_batch_chunks}
        draftChunkConc={draft.chunk_max_concurrent}
        onDraftConcurrency={(n) => setDraft({ ...draft, max_embedding_concurrency: n })}
        onDraftBatch={(n) => setDraft({ ...draft, llama_batch_size: n })}
        onDraftIndexBatch={(n) => setDraft({ ...draft, index_embed_batch_chunks: n })}
        onDraftChunkConc={(n) => setDraft({ ...draft, chunk_max_concurrent: n })}
        isOllama={showOllamaSections}
      />

      <SaveAndRestartDialog
        open={confirmOpen}
        onOpenChange={(next) => (!isPending ? setConfirmOpen(next) : null)}
        onConfirm={onConfirm}
        isPending={isPending}
        changes={changes}
      />
    </div>
  );
}
