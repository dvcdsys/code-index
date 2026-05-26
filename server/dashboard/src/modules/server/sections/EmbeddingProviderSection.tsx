import { useEffect, useMemo, useState } from 'react';
import { AlertCircle, CheckCircle2, Info, Loader2, Save } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Label } from '@/ui/label';
import type { EmbeddingProviderKind, EmbeddingProviderSecretEnv } from '@/api/types';
import {
  useActiveProvider,
  useEmbeddingProviders,
  useSwitchProvider,
  useTestProvider,
} from '../hooks';
import { OpenAIProviderForm, type OpenAIConfig, defaultOpenAIConfig } from './providers/OpenAIProviderForm';
import { VoyageProviderForm, type VoyageConfig, defaultVoyageConfig } from './providers/VoyageProviderForm';

// EmbeddingProviderSection wraps the provider-kind dropdown + the
// per-kind form. The ollama-specific sections (EmbeddingModelSection,
// RuntimeParamsSection, SidecarSection) stay rendered by the parent
// ServerPage when the active kind is "ollama" — switching to a remote
// provider hides them in ServerPage by checking activeProvider.kind.
//
// Save flow:
//   1. POST /admin/embedding-providers/{kind}/test with the draft.
//   2. On success → PUT /admin/embedding-providers/active.
//   3. Surface toast + invalidate caches so the footer / sidecar
//      cards update immediately.
//
// API keys are never stored on the server: configs only carry the
// NAME of the env var that holds the key. When the relevant env var
// is missing the form renders a red banner and the Save button is
// disabled.
export function EmbeddingProviderSection() {
  const providers = useEmbeddingProviders();
  const active = useActiveProvider();
  const switchMut = useSwitchProvider();

  const [draftKind, setDraftKind] = useState<EmbeddingProviderKind>('ollama');
  const [openAIDraft, setOpenAIDraft] = useState<OpenAIConfig>(defaultOpenAIConfig);
  const [voyageDraft, setVoyageDraft] = useState<VoyageConfig>(defaultVoyageConfig);

  // When the persisted active provider loads / changes (e.g. after a
  // successful switch), reset the drafts so the form mirrors what is
  // live. Selecting a different kind in the dropdown only changes the
  // form being rendered — it does NOT mutate the underlying drafts
  // until the admin clicks Save.
  useEffect(() => {
    const data = active.data;
    if (!data?.kind) return;
    setDraftKind(data.kind as EmbeddingProviderKind);
    const cfg = (data.config ?? {}) as Record<string, unknown>;
    if (data.kind === 'openai') {
      setOpenAIDraft({
        base_url: String(cfg.base_url ?? defaultOpenAIConfig.base_url),
        model: String(cfg.model ?? defaultOpenAIConfig.model),
        api_key_env: String(cfg.api_key_env ?? defaultOpenAIConfig.api_key_env),
        dimensions: typeof cfg.dimensions === 'number' ? cfg.dimensions : undefined,
      });
    }
    if (data.kind === 'voyage') {
      setVoyageDraft({
        model: String(cfg.model ?? defaultVoyageConfig.model),
        api_key_env: String(cfg.api_key_env ?? defaultVoyageConfig.api_key_env),
        output_dimension: Number(cfg.output_dimension ?? defaultVoyageConfig.output_dimension),
        output_dtype:
          (cfg.output_dtype as 'float' | 'int8') ?? defaultVoyageConfig.output_dtype,
        truncation: cfg.truncation !== false,
      });
    }
  }, [active.data]);

  // Lookup the env-key readiness for the currently selected kind so
  // the relevant form can render a "set CIX_VOYAGE_API_KEY before
  // saving" banner without each form duplicating the query.
  const envsForKind = useMemo<EmbeddingProviderSecretEnv[]>(() => {
    if (!providers.data) return [];
    return providers.data.providers.find((p) => p.kind === draftKind)?.secret_envs ?? [];
  }, [providers.data, draftKind]);

  const test = useTestProvider(draftKind);

  if (providers.isLoading || active.isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Embedding provider</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-sm text-muted-foreground">Loading providers…</div>
        </CardContent>
      </Card>
    );
  }
  if (providers.error || !providers.data || active.error) {
    return (
      <Alert variant="destructive">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>Could not load embedding providers</AlertTitle>
        <AlertDescription>
          {String(providers.error ?? active.error ?? 'unknown error')}
        </AlertDescription>
      </Alert>
    );
  }

  // Build the current draft config blob for the selected kind.
  // For ollama we always send an empty object — the backend's
  // SwitchEmbeddingProvider handler synthesizes a complete ollama
  // config from runtime-cfg + env on receipt, because the
  // ollama-specific tuning fields (GGUF model, ctx, GPU layers,
  // sidecar paths) are not part of this card's form.
  const draftConfig: Record<string, unknown> = (() => {
    switch (draftKind) {
      case 'openai':
        return { ...openAIDraft };
      case 'voyage':
        return { ...voyageDraft };
      case 'ollama':
        return {};
    }
  })();

  // Validation: we let the backend's /test endpoint be the source of
  // truth, but disable the Save button locally when an obviously
  // required field is empty or a referenced env var is missing.
  const allEnvsSet = envsForKind.every((e) => e.set);
  const localValid = (() => {
    if (draftKind === 'openai') {
      return !!openAIDraft.base_url && !!openAIDraft.model && !!openAIDraft.api_key_env;
    }
    if (draftKind === 'voyage') {
      return !!voyageDraft.model && !!voyageDraft.api_key_env;
    }
    return true; // ollama is edited via the lower sections
  })();

  const canSave = localValid && allEnvsSet && !switchMut.isPending && !test.isPending;
  // Dirty when the kind has changed; for remote providers also dirty
  // when the per-kind form differs from what's persisted. Ollama-
  // is-ollama is never dirty (form has no editable fields here —
  // those live in the sections below).
  const kindChanged = draftKind !== active.data?.kind;
  const dirty = kindChanged || (() => {
    if (draftKind === 'ollama') return false;
    const a = JSON.stringify(active.data?.config ?? {});
    const b = JSON.stringify(draftConfig);
    return a !== b;
  })();

  async function onSave() {
    try {
      // Skip the /test pre-check when switching to ollama — the
      // backend builds the full config from runtime-cfg + env on
      // receipt, so the client's empty {} can't be tested as-is
      // (would fail factory validation: model is required).
      // Ollama config correctness will be exercised by Start()
      // inside SwitchProvider anyway.
      if (draftKind !== 'ollama') {
        await test.mutateAsync(draftConfig);
      }
      await switchMut.mutateAsync({ kind: draftKind, config: draftConfig });
      toast.success(`Switched to ${draftKind}`, {
        description: 'Every project will get a Stale-model badge until reindex.',
      });
    } catch (e) {
      const detail = e instanceof ApiError ? e.detail : String(e);
      toast.error('Provider switch failed', { description: detail });
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Embedding provider
          {active.data?.kind ? (
            <span className="rounded-md bg-muted px-2 py-0.5 text-xs font-mono">
              {active.data.kind}
            </span>
          ) : null}
        </CardTitle>
        <CardDescription>
          Choose where embeddings are computed. Switching providers triggers a
          full reindex per project on the next clone job — every project's
          stored model fingerprint becomes stale.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <Alert>
          <Info className="h-4 w-4" />
          <AlertTitle>Cost & rate limits — read before picking</AlertTitle>
          <AlertDescription>
            <ul className="ml-4 mt-1 list-disc space-y-1 text-sm">
              <li>
                <strong>Ollama</strong> — free, runs the llama-server sidecar
                locally on this machine's CPU/GPU. No external API, no rate
                limits, no API keys.
              </li>
              <li>
                <strong>OpenAI-compatible</strong> — pay-as-you-go on{' '}
                <a
                  href="https://platform.openai.com/account/billing"
                  target="_blank"
                  rel="noreferrer noopener"
                  className="underline"
                >
                  api.openai.com
                </a>{' '}
                (account billing required) or free against your own
                self-hosted vLLM / TEI / LocalAI instance.
              </li>
              <li>
                <strong>Voyage AI</strong> — paid plan strongly recommended.
                The{' '}
                <a
                  href="https://dashboard.voyageai.com/"
                  target="_blank"
                  rel="noreferrer noopener"
                  className="underline"
                >
                  free tier
                </a>{' '}
                is capped at 3 RPM / 10K TPM — fine for a smoke test, not
                usable for indexing a real repo. Add a payment method
                before pointing the indexer at it.
              </li>
            </ul>
          </AlertDescription>
        </Alert>

        <div className="space-y-1.5">
          <Label htmlFor="provider-kind">Provider</Label>
          <select
            id="provider-kind"
            value={draftKind}
            onChange={(e) => setDraftKind(e.target.value as EmbeddingProviderKind)}
            className="block w-full rounded-md border bg-background px-3 py-2 text-sm sm:max-w-xs"
          >
            <option value="ollama">Ollama sidecar (local llama-server, free)</option>
            <option value="openai">OpenAI-compatible (/v1/embeddings, paid)</option>
            <option value="voyage">Voyage AI (paid plan recommended)</option>
          </select>
        </div>

        {draftKind === 'openai' ? (
          <OpenAIProviderForm
            value={openAIDraft}
            onChange={setOpenAIDraft}
            secretEnvs={envsForKind}
          />
        ) : null}
        {draftKind === 'voyage' ? (
          <VoyageProviderForm
            value={voyageDraft}
            onChange={setVoyageDraft}
            secretEnvs={envsForKind}
          />
        ) : null}
        {draftKind === 'ollama' ? (
          <div className="rounded-md border border-dashed bg-muted/30 p-3 text-xs text-muted-foreground">
            {kindChanged ? (
              <>
                Switching back to Ollama will restart the llama-server
                sidecar with the current model + tuning from the runtime
                config (see the sections below). After the switch, every
                project will need to be reindexed (full reindex on the
                next clone job).
              </>
            ) : (
              <>
                Ollama tuning (model picker, ctx, GPU layers, sidecar
                status) is configured in the sections below.
              </>
            )}
          </div>
        ) : null}

        <div className="flex items-center gap-2 pt-2">
          <Button onClick={onSave} disabled={!canSave || !dirty}>
            {switchMut.isPending || test.isPending ? (
              <Loader2 className="mr-1 h-4 w-4 animate-spin" />
            ) : (
              <Save className="mr-1 h-4 w-4" />
            )}
            {kindChanged ? `Save & switch to ${draftKind}` : 'Save & switch'}
          </Button>
          {test.isSuccess && !switchMut.isPending && draftKind !== 'ollama' ? (
            <span className="flex items-center gap-1 text-xs text-emerald-700">
              <CheckCircle2 className="h-3 w-3" /> Last test ok
            </span>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}
