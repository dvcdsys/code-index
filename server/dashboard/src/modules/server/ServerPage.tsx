import { useEffect, useMemo, useState, type ReactNode } from 'react';
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/ui/tabs';
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

type Tab = 'runtime' | 'resources';

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

  const [tab, setTab] = useState<Tab>('runtime');
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

  // Why the save button is greyed out, in the order the reasons apply. A
  // disabled control with no explanation reads as a broken one — which is
  // exactly how the embeddings-disabled case came across, since that state
  // blocks saving no matter how many fields you have edited.
  //
  // Note this covers THIS button only. Switching provider is a separate
  // control inside the provider card and stays enabled — though with the
  // service disabled the server refuses that too (embeddings.ErrDisabled),
  // because there is no service to switch. Both roads lead back to the env
  // var, so the message says so rather than leaving the reader hunting.
  const saveBlockedReason = disabled
    ? 'Embeddings are off, so there is nothing to apply this to — set CIX_EMBEDDINGS_ENABLED=true and restart the server'
    : isPending
      ? 'Applying the configuration…'
      : null;

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

  // Error is checked BEFORE the loading branch. The draft is only ever built
  // from a successful fetch, so `!draft` is also true on failure — testing it
  // first meant a failed config load rendered skeletons forever and the error
  // callout below was unreachable.
  let runtimeBody: ReactNode;
  if (cfg.error || (!cfg.isLoading && !cfg.data)) {
    runtimeBody = (
      <Callout variant="danger">
        <b>Could not load the runtime config</b>
        <p>{cfg.error instanceof ApiError ? cfg.error.detail : String(cfg.error)}</p>
      </Callout>
    );
  } else if (cfg.isLoading || !draft || !cfg.data) {
    runtimeBody = (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-40" />
        <Skeleton className="h-64" />
      </div>
    );
  } else {
    runtimeBody = (
      <>
        {disabled ? (
          <Callout variant="warn" className="mb-5">
            <b>Embeddings were disabled at boot</b>
            <p>
              The server started with <Chip>CIX_EMBEDDINGS_ENABLED=false</Chip>. Restart it with
              the variable set to <Chip>true</Chip> to enable runtime config and the sidecar.
            </p>
          </Callout>
        ) : null}

        {/* The action sits with the form it acts on, and sticks to the top of
            the tab so it cannot be scrolled away — the reason it used to live
            in the page header. It stays mounted while disabled rather than
            disappearing, because "greyed out with a reason" answers "why can't
            I save?" and an absent button does not. */}
        <div
          className="sticky top-0 z-10 mb-5 flex items-center gap-3 border-b py-2.5"
          // The scroll container has no background of its own, so a sticky
          // child needs the page canvas explicitly or the cards show through.
          style={{ background: 'var(--cix-canvas)' }}
        >
          <span className="cix-hint">
            {saveBlockedReason ??
              (dirty
                ? `${changes.length} unsaved change${changes.length === 1 ? '' : 's'}`
                : 'no changes')}
          </span>
          <Button
            variant="primary"
            className="ml-auto"
            onClick={() => setConfirmOpen(true)}
            disabled={!dirty || isPending || disabled}
            title={saveBlockedReason ?? 'Write the overrides and restart the sidecar'}
          >
            {isPending ? <Dots /> : null}
            {isOllama ? 'Save & restart' : 'Save'}
          </Button>
        </div>

        {runtimeGrid(cfg.data, draft)}
      </>
    );
  }

  return (
    <Page
      title="Server"
      subtitle="Embedding provider, indexing parameters and sidecar lifecycle, plus what the process is using on disk and in memory."
      // No header action: the only one this page has belongs to the Runtime
      // settings form and now lives with it, inside that tab.
    >
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          <TabsTrigger value="runtime">
            Runtime settings
            {/* Unsaved edits survive a tab switch, but the Save button does
                not follow — so the tab that owns them has to say so. */}
            {dirty ? <span className="cix-dot is-busy ml-1.5" aria-label="unsaved changes" /> : null}
          </TabsTrigger>
          <TabsTrigger value="resources">Resources</TabsTrigger>
        </TabsList>

        <TabsContent value="runtime">{runtimeBody}</TabsContent>

        {/* Provider-independent, and independent of the runtime-config query:
            memory and disk are worth reading precisely when the rest of this
            page is failing to load. */}
        <TabsContent value="resources">
          <ResourcesSection />
        </TabsContent>
      </Tabs>

      <SaveAndRestartDialog
        open={confirmOpen}
        onOpenChange={(next) => (!isPending ? setConfirmOpen(next) : null)}
        onConfirm={onConfirm}
        isPending={isPending}
        changes={changes}
      />
    </Page>
  );

  // Declared as a closure rather than a component so the draft handlers stay
  // where the draft lives, without threading a dozen props through a wrapper.
  function runtimeGrid(config: RuntimeConfig, d: Draft) {
    return (
      <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div className="flex min-w-0 flex-col gap-5">
          <EmbeddingProviderSection />

          {/* Ollama-only cards. For openai/voyage there is no GGUF, no child
              process to restart, no GPU layers or threads — the provider form
              above is the whole edit surface. */}
          {isOllama ? (
            <>
              <EmbeddingModelSection
                config={config}
                draftModel={d.embedding_model}
                onDraftChange={(v) => setDraft({ ...d, embedding_model: v })}
              />
              <RuntimeParamsSection
                config={config}
                draftCtx={d.llama_ctx_size}
                draftGpuLayers={d.llama_n_gpu_layers}
                draftThreads={d.llama_n_threads}
                draftCacheRAM={d.llama_cache_ram_mib}
                onDraftCtx={(n) => setDraft({ ...d, llama_ctx_size: n })}
                onDraftGpuLayers={(n) => setDraft({ ...d, llama_n_gpu_layers: n })}
                onDraftThreads={(n) => setDraft({ ...d, llama_n_threads: n })}
                onDraftCacheRAM={(n) => setDraft({ ...d, llama_cache_ram_mib: n })}
              />
            </>
          ) : null}

          {/* Throughput is always shown: the queue cap is a Service-level
              limit on parallel /v1/embeddings POSTs and every provider
              honours it. Only the llama batch field inside is ollama-gated. */}
          <AdvancedSection
            config={config}
            draftConcurrency={d.max_embedding_concurrency}
            draftBatch={d.llama_batch_size}
            draftIndexBatch={d.index_embed_batch_chunks}
            draftChunkConc={d.chunk_max_concurrent}
            onDraftConcurrency={(n) => setDraft({ ...d, max_embedding_concurrency: n })}
            onDraftBatch={(n) => setDraft({ ...d, llama_batch_size: n })}
            onDraftIndexBatch={(n) => setDraft({ ...d, index_embed_batch_chunks: n })}
            onDraftChunkConc={(n) => setDraft({ ...d, chunk_max_concurrent: n })}
            isOllama={isOllama}
          />
        </div>

        {isOllama ? (
          <div className="flex flex-col gap-5 xl:sticky xl:top-0">
            <SidecarRail />
          </div>
        ) : null}
      </div>
    );
  }
}
