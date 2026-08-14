import { Callout } from '@/ui/alert';
import { Chip } from '@/ui/code';
import { Field, Input } from '@/ui/input';
import type { EmbeddingProviderSecretEnv } from '@/api/types';

// Mirrors the openai provider's persisted config blob
// (server/internal/embeddings/provider/openai/openai.go).
export interface OpenAIConfig {
  base_url: string;
  model: string;
  api_key_env: string;
  dimensions?: number;
}

export const defaultOpenAIConfig: OpenAIConfig = {
  base_url: 'https://api.openai.com',
  model: 'text-embedding-3-small',
  api_key_env: 'CIX_OPENAI_API_KEY',
};

interface Props {
  value: OpenAIConfig;
  onChange: (next: OpenAIConfig) => void;
  secretEnvs: EmbeddingProviderSecretEnv[];
}

// Suggestions only — free text stays the source of truth because any string
// is valid against a self-hosted server.
const SUGGESTED_MODELS = [
  'text-embedding-3-small',
  'text-embedding-3-large',
  'text-embedding-ada-002',
];

export function OpenAIProviderForm({ value, onChange, secretEnvs }: Props) {
  const apiKeyEnv = secretEnvs.find((e) => e.name === value.api_key_env);
  const apiKeyMissing = apiKeyEnv != null && !apiKeyEnv.set;

  return (
    <div className="flex flex-col gap-4">
      <Field
        label="Base URL"
        htmlFor="openai-base-url"
        hint="Origin without the trailing /v1. Works for OpenAI proper, vLLM, TEI, LocalAI and anything else speaking /v1/embeddings."
      >
        <Input
          id="openai-base-url"
          value={value.base_url}
          onChange={(e) => onChange({ ...value, base_url: e.target.value })}
          placeholder="https://api.openai.com"
        />
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Model"
          htmlFor="openai-model"
          hint="Self-hosted servers: whichever name that server expects."
        >
          <Input
            id="openai-model"
            list="openai-model-suggestions"
            value={value.model}
            onChange={(e) => onChange({ ...value, model: e.target.value })}
          />
          <datalist id="openai-model-suggestions">
            {SUGGESTED_MODELS.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
        </Field>

        <Field
          label="Dimensions"
          htmlFor="openai-dim"
          hint="Matryoshka shrink for text-embedding-3-*. Empty uses the native dimension."
        >
          <Input
            id="openai-dim"
            type="number"
            min={0}
            value={value.dimensions ?? ''}
            onChange={(e) =>
              onChange({
                ...value,
                dimensions: e.target.value === '' ? undefined : Number(e.target.value),
              })
            }
            placeholder="server default"
          />
        </Field>
      </div>

      <Field
        label="API key env var"
        htmlFor="openai-key-env"
        hint="The dashboard never stores the key. The server reads this variable live on every embed call."
      >
        <Input
          id="openai-key-env"
          value={value.api_key_env}
          onChange={(e) => onChange({ ...value, api_key_env: e.target.value })}
          placeholder="CIX_OPENAI_API_KEY"
          invalid={apiKeyMissing}
        />
      </Field>

      {apiKeyMissing ? (
        <Callout variant="danger">
          <b>That env var is not set on the server</b>
          <p>
            Export <Chip>{value.api_key_env}</Chip> (compose, Portainer, systemd…) and restart the
            container before saving — every call would fail until the key exists.
          </p>
        </Callout>
      ) : null}

      <Callout variant="warn">
        <b>No retry-with-backoff yet</b>
        <p>
          The upstream HTTP status is forwarded as-is. On 429s, lower the queue concurrency in
          Throughput or move to an account tier with a higher RPM. Self-hosted servers (vLLM, TEI,
          LocalAI) usually don't rate-limit at all.
        </p>
      </Callout>
    </div>
  );
}
