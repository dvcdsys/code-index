import { AlertTriangle, ExternalLink, Info } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { Switch } from '@/ui/switch';
import type { EmbeddingProviderSecretEnv } from '@/api/types';

// VoyageConfig mirrors the voyage provider's persisted config blob
// shape (see server/internal/embeddings/provider/voyage/voyage.go).
export interface VoyageConfig {
  model: string;
  api_key_env: string;
  output_dimension: number;
  output_dtype: 'float' | 'int8';
  truncation: boolean;

  // Operator-supplied rate-limit caps. 0 = no client-side throttling
  // (the server will only react to upstream 429/400). Sourced from
  // the operator's Voyage dashboard Rate Limits page; we can't fetch
  // them programmatically (Voyage has no API for limits).
  rate_limit_rpm?: number;
  rate_limit_tpm?: number;
  max_inputs_per_request?: number;
  max_tokens_per_request?: number;
}

export const defaultVoyageConfig: VoyageConfig = {
  model: 'voyage-code-3',
  api_key_env: 'CIX_VOYAGE_API_KEY',
  output_dimension: 1024,
  output_dtype: 'float',
  truncation: true,
};

interface Props {
  value: VoyageConfig;
  onChange: (next: VoyageConfig) => void;
  secretEnvs: EmbeddingProviderSecretEnv[];
}

const MODELS = [
  'voyage-code-3',
  'voyage-3-large',
  'voyage-3',
  'voyage-3-lite',
  'voyage-code-2',
];

const DIMENSIONS = [256, 512, 1024, 2048];

// numberOrUndef parses a number input; empty / NaN / negative → undefined
// so the field round-trips to "unset" (no client-side enforcement).
function numberOrUndef(v: string): number | undefined {
  if (v.trim() === '') return undefined;
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return n;
}

export function VoyageProviderForm({ value, onChange, secretEnvs }: Props) {
  const apiKeyEnv = secretEnvs.find((e) => e.name === value.api_key_env);
  const apiKeyMissing = apiKeyEnv != null && !apiKeyEnv.set;

  return (
    <div className="space-y-4">
      <Alert>
        <Info className="h-4 w-4" />
        <AlertTitle>Rate limits — fill in from your Voyage dashboard</AlertTitle>
        <AlertDescription>
          Voyage doesn't expose per-account rate limits via API, so the
          server can't fetch yours automatically. Open the{' '}
          <a
            href="https://dashboard.voyageai.com/"
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1 underline"
          >
            Voyage dashboard <ExternalLink className="h-3 w-3" />
          </a>{' '}
          → Rate Limits, copy your tier's numbers into the fields below,
          and the indexer will throttle itself accordingly via a
          token-bucket. Leave all four blank to disable client-side
          throttling (the server will only react to upstream 429/400).
        </AlertDescription>
      </Alert>

      <div className="space-y-1.5">
        <Label htmlFor="voyage-model">Model</Label>
        <select
          id="voyage-model"
          value={value.model}
          onChange={(e) => onChange({ ...value, model: e.target.value })}
          className="block w-full rounded-md border bg-background px-3 py-2 text-sm sm:max-w-sm"
        >
          {MODELS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="voyage-dim">Output dimension (Matryoshka)</Label>
          <select
            id="voyage-dim"
            value={String(value.output_dimension)}
            onChange={(e) => onChange({ ...value, output_dimension: Number(e.target.value) })}
            className="block w-full rounded-md border bg-background px-3 py-2 text-sm"
          >
            {DIMENSIONS.map((d) => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
          <p className="text-xs text-muted-foreground">
            Changing this triggers a full reindex per project.
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="voyage-dtype">Output dtype</Label>
          <select
            id="voyage-dtype"
            value={value.output_dtype}
            onChange={(e) =>
              onChange({ ...value, output_dtype: e.target.value as 'float' | 'int8' })
            }
            className="block w-full rounded-md border bg-background px-3 py-2 text-sm"
          >
            <option value="float">float (default)</option>
            <option value="int8">int8 (dequantized server-side)</option>
          </select>
          <p className="text-xs text-muted-foreground">
            <code>binary</code> / <code>ubinary</code> are not supported — the
            vector store has no hamming-distance search.
          </p>
        </div>
      </div>

      {/* Rate-limit fields. All four optional. Defaults in the comment
          below mirror the public docs; the operator should override
          per their actual tier on the Voyage dashboard. */}
      <fieldset className="space-y-3 rounded-md border bg-muted/20 p-3">
        <legend className="px-1 text-sm font-medium">Rate limits (from your Voyage dashboard)</legend>
        <p className="text-xs text-muted-foreground">
          Public-docs Tier 1 baseline (multiply by ×2 / ×3 for Tier 2 / Tier
          3 spend):{' '}
          <code>voyage-code-*</code> = 2000 RPM / 3M TPM / 128 inputs /
          120K tokens per request.{' '}
          <code>voyage-3*</code> = 2000 RPM / 3–16M TPM / 1000 inputs /
          120K tokens per request. Free tier = 3 RPM / 10K TPM regardless
          of model.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1">
            <Label htmlFor="voyage-rpm">Requests per minute (RPM)</Label>
            <Input
              id="voyage-rpm"
              type="number"
              min={0}
              placeholder="e.g. 2000 (Tier 1 baseline)"
              value={value.rate_limit_rpm ?? ''}
              onChange={(e) => onChange({ ...value, rate_limit_rpm: numberOrUndef(e.target.value) })}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="voyage-tpm">Tokens per minute (TPM)</Label>
            <Input
              id="voyage-tpm"
              type="number"
              min={0}
              placeholder="e.g. 3000000"
              value={value.rate_limit_tpm ?? ''}
              onChange={(e) => onChange({ ...value, rate_limit_tpm: numberOrUndef(e.target.value) })}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="voyage-max-inputs">Max inputs per request</Label>
            <Input
              id="voyage-max-inputs"
              type="number"
              min={0}
              placeholder="128 for code-*, 1000 for voyage-3*"
              value={value.max_inputs_per_request ?? ''}
              onChange={(e) => onChange({ ...value, max_inputs_per_request: numberOrUndef(e.target.value) })}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="voyage-max-tokens">Max tokens per request</Label>
            <Input
              id="voyage-max-tokens"
              type="number"
              min={0}
              placeholder="e.g. 100000 (Voyage hard cap 120K)"
              value={value.max_tokens_per_request ?? ''}
              onChange={(e) => onChange({ ...value, max_tokens_per_request: numberOrUndef(e.target.value) })}
            />
          </div>
        </div>
        <p className="text-[10px] text-muted-foreground">
          Empty = no client-side enforcement for that field. RPM/TPM
          empty means the indexer doesn't throttle itself (you'll see
          429s on overflow); per-request fields empty fall back to safe
          defaults (128 inputs / ~100K tokens).
        </p>
      </fieldset>

      <div className="flex items-center gap-3">
        <Switch
          id="voyage-truncation"
          checked={value.truncation}
          onCheckedChange={(c) => onChange({ ...value, truncation: c === true })}
        />
        <Label htmlFor="voyage-truncation" className="cursor-pointer">
          Truncate over-length inputs server-side
        </Label>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="voyage-key-env">API key env var</Label>
        <Input
          id="voyage-key-env"
          value={value.api_key_env}
          onChange={(e) => onChange({ ...value, api_key_env: e.target.value })}
          placeholder="CIX_VOYAGE_API_KEY"
        />
        <p className="text-xs text-muted-foreground">
          The dashboard never stores the key — only this env-var name.
        </p>
      </div>

      {apiKeyMissing ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>API key env var is not set</AlertTitle>
          <AlertDescription>
            Export <code>{value.api_key_env}</code> on the server and restart
            the container before saving. Voyage API calls would fail until
            the key becomes available.
          </AlertDescription>
        </Alert>
      ) : null}
    </div>
  );
}
