import { useEffect, useMemo, useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { RuntimeConfig, RuntimeConfigUpdate } from '@/api/types';
import { useStatusFact } from '@/app/StatusBar';
import { useServerStatus } from '@/lib/useServerStatus';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Chip } from '@/ui/code';
import { Page } from '@/ui/page';
import { Skeleton } from '@/ui/skeleton';
import {
  useRestartSidecar,
  useRuntimeConfig,
  useSidecarStatus,
  useUpdateRuntimeConfig,
} from './hooks';
import { EmbeddingModelSection } from './sections/EmbeddingModelSection';
import { RuntimeParamsSection } from './sections/RuntimeParamsSection';
import { SidecarRail } from './sections/SidecarRail';
import { AdvancedSection } from './sections/AdvancedSection';
import { EmbeddingProviderSection } from './sections/EmbeddingProviderSection';
import { ResourcesSection } from './sections/ResourcesSection';
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

const NUMERIC_FIELDS = [
  'llama_ctx_size',
  'llama_n_gpu_layers',
  'llama_n_threads',
  'max_embedding_concurrency',
  'llama_batch_size',
  'index_embed_batch_chunks',
  'chunk_max_concurrent',
  'llama_cache_ram_mib',
] as const;

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

// Produces the partial PUT body (changed fields only) and the human-readable
// diff the confirm dialog renders.
function diffPatch(
  c: RuntimeConfig,
  d: Draft
): { patch: RuntimeConfigUpdate; changes: Array<{ field: string; from: string; to: string }> } {
  const patch: RuntimeConfigUpdate = {};
  const changes: Array<{ field: string; from: string; to: string }> = [];
  if (d.embedding_model !== c.embedding_model) {
    patch.embedding_model = d.embedding_model;
    changes.push({ field: 'embedding_model', from: c.embedding_model, to: d.embedding_model });
  }
  for (const k of NUMERIC_FIELDS) {
    if (d[k] !== c[k]) {
      patch[k] = d[k];
      changes.push({ field: k, from: String(c[k]), to: String(d[k]) });
    }
  }
  return { patch, changes };
}

// Two columns: config cards on the left, live runtime on the right. The
// sidecar's state has to stay visible while the fields that will restart it
// are being edited, which is exactly what a right rail is for.
//
// One primary action lives in the page header ("Save & restart"). The
// provider card keeps its own inline "Save & switch" because switching
// provider is a different, heavier operation than tuning flags.
export default function ServerPage() {
  const cfg = useRuntimeConfig();
  const status = useSidecarStatus();
  const update = useUpdateRuntimeConfig();
  const restart = useRestartSidecar();

  // /status is already polled for the status bar, and its embedding_provider
  // field is the LIVE active provider — the right signal for "show the ollama
  // cards?". Defaulting to ollama while it loads avoids a flash of empty page.
  //
  // The catch: "live" means Service.CurrentKind(), which returns "" whenever
  // the provider is nil — precisely the window in which the sidecar is being
  // torn down and rebuilt. So every Save & restart made /status report an
  // empty kind, and `?? 'ollama'` does not catch "": the page decided it was
  // talking to a remote provider and unmounted the model card, the runtime
  // params card and the sidecar rail. Worse, useRestartSidecar invalidates
  // this very query on settle, so the refetch lands at the emptiest possible
  // moment and the answer then sits in cache until the 30s poll — which is
  // why the cards only came back on a reload.
  //
  // "" is *unknown*, not *remote*. Hold the last kind we actually saw and
  // only ever switch layout on a positively-reported one.
  const serverStatus = useServerStatus();
  const reportedKind = serverStatus.data?.embedding_provider;
  const [lastKnownKind, setLastKnownKind] = useState<string | null>(null);
  useEffect(() => {
    if (reportedKind) setLastKnownKind(reportedKind);
  }, [reportedKind]);
  const activeKind = reportedKind || lastKnownKind || 'ollama';
  const isOllama = activeKind === 'ollama';

  const [draft, setDraft] = useState<Draft | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);

  // Initialise / reset the draft whenever the server-side config changes
  // under us (first fetch, refresh after save).
  useEffect(() => {
    if (cfg.data) setDraft(configToDraft(cfg.data));
  }, [cfg.data]);

  const changes = useMemo(
    () => (cfg.data && draft ? diffPatch(cfg.data, draft).changes : []),
    [cfg.data, draft]
  );
  const dirty = changes.length > 0;

  useStatusFact(dirty ? 'unsaved changes' : null);

  const isPending = update.isPending || restart.isPending;
  const disabled = status.data?.state === 'disabled';

  async function onConfirm() {
    if (!cfg.data || !draft) return;
    const { patch } = diffPatch(cfg.data, draft);
    try {
      // 1. Write overrides. The mutation refreshes the cache so the DB pills
      //    flip before the restart fires.
      if (Object.keys(patch).length > 0) await update.mutateAsync(patch);
      // 2. Restart so the new model / flags actually load.
      await restart.mutateAsync();
      setConfirmOpen(false);
      toast.success('Configuration saved', {
        description: 'The sidecar is restarting — watch the rail on the right.',
      });
    } catch (e) {
      toast.error('Save & restart failed', {
        description: e instanceof ApiError ? e.detail : String(e),
      });
    }
  }

  if (cfg.isLoading || !draft) {
    return (
      <Page title="Server" subtitle="Embedding provider, model, indexing parameters, sidecar.">
        <div className="flex flex-col gap-4">
          <Skeleton className="h-40" />
          <Skeleton className="h-64" />
        </div>
      </Page>
    );
  }

  if (cfg.error || !cfg.data) {
    return (
      <Page title="Server" subtitle="Embedding provider, model, indexing parameters, sidecar.">
        <Callout variant="danger">
          <b>Could not load the runtime config</b>
          <p>{cfg.error instanceof ApiError ? cfg.error.detail : String(cfg.error)}</p>
        </Callout>
      </Page>
    );
  }

  return (
    <Page
      title="Server"
      subtitle={
        isOllama
          ? 'Embedding provider, model, indexing parameters and sidecar lifecycle. Saved overrides land in the database and are reapplied on the next restart — env vars stay as bootstrap defaults.'
          : 'Embedding provider and the server-wide concurrency cap every provider honours. The provider form is the main edit surface for remote backends.'
      }
      action={
        <Button
          variant="primary"
          onClick={() => setConfirmOpen(true)}
          disabled={!dirty || isPending || disabled}
        >
          {isPending ? <Dots /> : null}
          {isOllama ? 'Save & restart' : 'Save'}
        </Button>
      }
    >
      {disabled ? (
        <Callout variant="warn" className="mb-5">
          <b>Embeddings were disabled at boot</b>
          <p>
            The server started with <Chip>CIX_EMBEDDINGS_ENABLED=false</Chip>. Restart it with the
            variable set to <Chip>true</Chip> to enable runtime config and the sidecar.
          </p>
        </Callout>
      ) : null}

      <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div className="flex min-w-0 flex-col gap-5">
          <EmbeddingProviderSection />

          {/* Ollama-only cards. For openai/voyage there is no GGUF, no child
              process to restart, no GPU layers or threads — the provider form
              above is the whole edit surface. */}
          {isOllama ? (
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
            </>
          ) : null}

          {/* Throughput is always shown: the queue cap is a Service-level
              limit on parallel /v1/embeddings POSTs and every provider
              honours it. Only the llama batch field inside is ollama-gated. */}
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
            isOllama={isOllama}
          />

          {/* Provider-independent: memory, disk and reclaimable garbage exist
              regardless of who computes the embeddings. */}
          <ResourcesSection />
        </div>

        {isOllama ? (
          <div className="flex flex-col gap-5 xl:sticky xl:top-0">
            <SidecarRail />
          </div>
        ) : null}
      </div>

      <SaveAndRestartDialog
        open={confirmOpen}
        onOpenChange={(next) => (!isPending ? setConfirmOpen(next) : null)}
        onConfirm={onConfirm}
        isPending={isPending}
        changes={changes}
      />
    </Page>
  );
}
