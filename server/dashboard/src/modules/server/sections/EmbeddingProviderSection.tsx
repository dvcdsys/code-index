import { useEffect, useMemo, useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { EmbeddingProviderKind, EmbeddingProviderSecretEnv } from '@/api/types';
import { Callout } from '@/ui/alert';
import { Badge, Status } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { Card, CardBody, CardHead } from '@/ui/card';
import { Field } from '@/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import {
  useActiveProvider,
  useEmbeddingProviders,
  useSwitchProvider,
  useTestProvider,
} from '../hooks';
import {
  OpenAIProviderForm,
  defaultOpenAIConfig,
  type OpenAIConfig,
} from './providers/OpenAIProviderForm';
import {
  VoyageProviderForm,
  defaultVoyageConfig,
  type VoyageConfig,
} from './providers/VoyageProviderForm';

// The provider-kind picker plus the per-kind form. Ollama's own tuning
// (model, ctx, GPU layers, sidecar) lives in the sections below, rendered by
// ServerPage only while ollama is the active kind.
//
// Save flow: POST /test with the draft → PUT /active on success → invalidate
// so the status bar and the sidecar rail update immediately. API keys are
// never sent here; configs carry only the NAME of the env var holding one.
export function EmbeddingProviderSection() {
  const providers = useEmbeddingProviders();
  const active = useActiveProvider();
  const switchMut = useSwitchProvider();

  const [draftKind, setDraftKind] = useState<EmbeddingProviderKind>('ollama');
  const [openAIDraft, setOpenAIDraft] = useState<OpenAIConfig>(defaultOpenAIConfig);
  const [voyageDraft, setVoyageDraft] = useState<VoyageConfig>(defaultVoyageConfig);

  // Reset the drafts when the persisted provider loads or changes so the form
  // mirrors what is live. Picking a different kind only swaps which form is
  // rendered — the drafts stay untouched until Save.
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
        output_dtype: (cfg.output_dtype as 'float' | 'int8') ?? defaultVoyageConfig.output_dtype,
        truncation: cfg.truncation !== false,
        rate_limit_rpm: typeof cfg.rate_limit_rpm === 'number' ? cfg.rate_limit_rpm : undefined,
        rate_limit_tpm: typeof cfg.rate_limit_tpm === 'number' ? cfg.rate_limit_tpm : undefined,
        max_inputs_per_request:
          typeof cfg.max_inputs_per_request === 'number' ? cfg.max_inputs_per_request : undefined,
        max_tokens_per_request:
          typeof cfg.max_tokens_per_request === 'number' ? cfg.max_tokens_per_request : undefined,
      });
    }
  }, [active.data]);

  // Env readiness for the selected kind, resolved once here so neither form
  // has to repeat the query.
  const envsForKind = useMemo<EmbeddingProviderSecretEnv[]>(() => {
    if (!providers.data) return [];
    return providers.data.providers.find((p) => p.kind === draftKind)?.secret_envs ?? [];
  }, [providers.data, draftKind]);

  const test = useTestProvider(draftKind);

  if (providers.isLoading || active.isLoading) {
    return (
      <Card>
        <CardHead title="Embedding provider" />
        <CardBody>
          <Skeleton className="h-[38px]" />
        </CardBody>
      </Card>
    );
  }

  if (providers.error || !providers.data || active.error) {
    return (
      <Callout variant="danger">
        <b>Could not load embedding providers</b>
        <p>{String(providers.error ?? active.error ?? 'unknown error')}</p>
      </Callout>
    );
  }

  // For ollama we always send {} — the backend synthesizes a complete config
  // from runtime-cfg + env on receipt, because ollama's tuning fields (GGUF
  // model, ctx, GPU layers, sidecar paths) are not part of this card.
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

  // The backend's /test is the real authority; this only disables Save when a
  // required field is empty or a referenced env var is missing.
  const allEnvsSet = envsForKind.every((e) => e.set);
  const localValid = (() => {
    if (draftKind === 'openai') {
      return !!openAIDraft.base_url && !!openAIDraft.model && !!openAIDraft.api_key_env;
    }
    if (draftKind === 'voyage') return !!voyageDraft.model && !!voyageDraft.api_key_env;
    return true; // ollama is edited in the sections below
  })();

  const canSave = localValid && allEnvsSet && !switchMut.isPending && !test.isPending;
  const kindChanged = draftKind !== active.data?.kind;
  const dirty =
    kindChanged ||
    (draftKind !== 'ollama' &&
      JSON.stringify(active.data?.config ?? {}) !== JSON.stringify(draftConfig));

  async function onSave() {
    try {
      // Skip /test when switching to ollama — the backend builds the full
      // config from runtime-cfg + env on receipt, so the client's empty {}
      // can't be validated as-is. Start() inside SwitchProvider exercises it.
      if (draftKind !== 'ollama') await test.mutateAsync(draftConfig);
      await switchMut.mutateAsync({ kind: draftKind, config: draftConfig });
      toast.success(`Switched to ${draftKind}`, {
        description: 'Every project shows a stale-model badge until it is reindexed.',
      });
    } catch (e) {
      toast.error('Provider switch failed', {
        description: e instanceof ApiError ? e.detail : String(e),
      });
    }
  }

  const pending = switchMut.isPending || test.isPending;

  return (
    <Card>
      <CardHead
        title="Embedding provider"
        aside={
          <span className="font-mono text-[11px] font-normal text-dim">reindex on change</span>
        }
      >
        {active.data?.kind ? <Badge variant="ink">{active.data.kind}</Badge> : null}
      </CardHead>
      <CardBody className="flex flex-col gap-5">
        <Callout>
          <b>Cost &amp; rate limits</b>
          <p>
            <b className="text-ink">Ollama</b> — local llama-server, free, no keys, no limits.{' '}
            <b className="text-ink">OpenAI-compatible</b> — pay-as-you-go on api.openai.com, or
            free against your own vLLM / TEI / LocalAI. <b className="text-ink">Voyage AI</b> — the
            free tier is 3 RPM, enough for a smoke test and nothing else; add a payment method
            before pointing the indexer at it.
          </p>
        </Callout>

        <div className="flex flex-wrap items-end gap-3">
          <Field label="Provider" className="min-w-[280px] flex-1">
            <Select
              value={draftKind}
              onValueChange={(v) => setDraftKind(v as EmbeddingProviderKind)}
            >
              <SelectTrigger aria-label="Embedding provider kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ollama">Ollama sidecar (local, free)</SelectItem>
                <SelectItem value="openai">OpenAI-compatible (/v1/embeddings, paid)</SelectItem>
                <SelectItem value="voyage">Voyage AI (paid plan recommended)</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Button variant="primary" onClick={onSave} disabled={!canSave || !dirty}>
            {pending ? <Dots /> : null}
            {kindChanged ? `Save & switch to ${draftKind}` : 'Save & switch'}
          </Button>
          {test.isSuccess && !switchMut.isPending && draftKind !== 'ollama' ? (
            <Status tone="ok" className="pb-2.5 font-mono text-[11.5px]">
              last test ok
            </Status>
          ) : null}
        </div>

        {draftKind === 'openai' ? (
          <OpenAIProviderForm value={openAIDraft} onChange={setOpenAIDraft} secretEnvs={envsForKind} />
        ) : null}
        {draftKind === 'voyage' ? (
          <VoyageProviderForm value={voyageDraft} onChange={setVoyageDraft} secretEnvs={envsForKind} />
        ) : null}
        {draftKind === 'ollama' ? (
          <p className="m-0 text-[13.5px] text-dim">
            {kindChanged
              ? 'Switching back restarts the llama-server sidecar with the model and tuning from the cards below. Every project needs a full reindex afterwards.'
              : 'Ollama tuning — model, context, GPU layers, sidecar state — is in the cards below.'}
          </p>
        ) : null}
      </CardBody>
    </Card>
  );
}
