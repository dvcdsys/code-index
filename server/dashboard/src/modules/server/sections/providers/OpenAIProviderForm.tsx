import { AlertTriangle } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import type { EmbeddingProviderSecretEnv } from '@/api/types';

// OpenAIConfig mirrors the openai provider's persisted config blob
// shape (see server/internal/embeddings/provider/openai/openai.go).
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

// Common OpenAI-compatible model picker entries. Free-text input
// stays the source of truth (any string is valid for self-hosted
// servers); these are just suggestions.
const SUGGESTED_MODELS = [
  'text-embedding-3-small',
  'text-embedding-3-large',
  'text-embedding-ada-002',
];

export function OpenAIProviderForm({ value, onChange, secretEnvs }: Props) {
  const apiKeyEnv = secretEnvs.find((e) => e.name === value.api_key_env);
  const apiKeyMissing = apiKeyEnv != null && !apiKeyEnv.set;

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="openai-base-url">Base URL</Label>
        <Input
          id="openai-base-url"
          value={value.base_url}
          onChange={(e) => onChange({ ...value, base_url: e.target.value })}
          placeholder="https://api.openai.com"
        />
        <p className="text-xs text-muted-foreground">
          Server origin without the trailing <code>/v1</code>. Works for OpenAI
          proper, vLLM, TEI, LocalAI, Ollama's openai endpoint, and any other
          OpenAI-compatible /v1/embeddings server.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="openai-model">Model</Label>
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
        <p className="text-xs text-muted-foreground">
          For self-hosted servers use whichever model name the server expects.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="openai-dim">Dimensions (optional)</Label>
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
          placeholder="(server default)"
        />
        <p className="text-xs text-muted-foreground">
          Matryoshka shrink for <code>text-embedding-3-*</code>. Leave empty
          to use the model's native dimension.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="openai-key-env">API key env var</Label>
        <Input
          id="openai-key-env"
          value={value.api_key_env}
          onChange={(e) => onChange({ ...value, api_key_env: e.target.value })}
          placeholder="CIX_OPENAI_API_KEY"
        />
        <p className="text-xs text-muted-foreground">
          The dashboard never stores the key itself. The server reads this
          env var live on every embed call.
        </p>
      </div>

      {apiKeyMissing ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>API key env var is not set</AlertTitle>
          <AlertDescription>
            Export <code>{value.api_key_env}</code> on the server (compose,
            portainer, systemd, …) and restart the container before saving.
            Calls would fail until the key becomes available.
          </AlertDescription>
        </Alert>
      ) : null}

      <p className="text-xs text-muted-foreground">
        Rate-limit handling: the server forwards the upstream HTTP status
        as-is — there is no retry-with-backoff yet. If you hit 429s, lower
        the concurrency in the Throughput card below or pick an account
        tier with higher RPM. Self-hosted servers (vLLM, TEI, LocalAI)
        typically don't rate-limit at all.
      </p>
    </div>
  );
}
