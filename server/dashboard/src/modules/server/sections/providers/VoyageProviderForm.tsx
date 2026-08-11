import { Callout } from '@/ui/alert';
import { Chip } from '@/ui/code';
import { Field, Input } from '@/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { SwitchRow } from '@/ui/switch';
import type { EmbeddingProviderSecretEnv } from '@/api/types';

// Mirrors the voyage provider's persisted config blob
// (server/internal/embeddings/provider/voyage/voyage.go).
export interface VoyageConfig {
  model: string;
  api_key_env: string;
  output_dimension: number;
  output_dtype: 'float' | 'int8';
  truncation: boolean;

  // Operator-supplied rate-limit caps. Unset = no client-side throttling; the
  // server then only reacts to upstream 429/400. Voyage has no API for
  // limits, so these come off the operator's dashboard by hand.
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

const MODELS = ['voyage-code-3', 'voyage-3-large', 'voyage-3', 'voyage-3-lite', 'voyage-code-2'];
const DIMENSIONS = [256, 512, 1024, 2048];

// Empty / NaN / negative → undefined, so a cleared field round-trips to
// "unset" instead of to zero.
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
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Model">
          <Select value={value.model} onValueChange={(v) => onChange({ ...value, model: v })}>
            <SelectTrigger aria-label="Voyage model">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {MODELS.map((m) => (
                <SelectItem key={m} value={m}>
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field label="Dimension" hint="Changing it forces a full reindex.">
          <Select
            value={String(value.output_dimension)}
            onValueChange={(v) => onChange({ ...value, output_dimension: Number(v) })}
          >
            <SelectTrigger aria-label="Output dimension">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DIMENSIONS.map((d) => (
                <SelectItem key={d} value={String(d)}>
                  {d}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field label="Output dtype" hint="binary / ubinary need hamming search — not supported.">
          <Select
            value={value.output_dtype}
            onValueChange={(v) => onChange({ ...value, output_dtype: v as 'float' | 'int8' })}
          >
            <SelectTrigger aria-label="Output dtype">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="float">float</SelectItem>
              <SelectItem value="int8">int8 (dequantized server-side)</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      <div className="border p-3.5">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="cix-label">Rate limits</span>
          <a
            href="https://dashboard.voyageai.com/"
            target="_blank"
            rel="noreferrer noopener"
            className="font-mono text-[11px] text-accent hover:underline"
          >
            voyage dashboard ↗
          </a>
        </div>
        <p className="mt-2 text-[13px] leading-snug text-dim">
          Voyage exposes no API for per-account limits, so copy your tier's numbers in and the
          indexer throttles itself with a token bucket. Tier 1 baseline:{' '}
          <Chip>voyage-code-*</Chip> 2000 RPM / 3M TPM / 128 inputs / 120K tokens per request. Free
          tier is 3 RPM / 10K TPM for every model.
        </p>
        <div className="mt-3.5 grid gap-3.5 sm:grid-cols-2">
          <Field label="Requests / minute" htmlFor="voyage-rpm">
            <Input
              id="voyage-rpm"
              type="number"
              min={0}
              placeholder="2000"
              value={value.rate_limit_rpm ?? ''}
              onChange={(e) =>
                onChange({ ...value, rate_limit_rpm: numberOrUndef(e.target.value) })
              }
            />
          </Field>
          <Field label="Tokens / minute" htmlFor="voyage-tpm">
            <Input
              id="voyage-tpm"
              type="number"
              min={0}
              placeholder="3000000"
              value={value.rate_limit_tpm ?? ''}
              onChange={(e) =>
                onChange({ ...value, rate_limit_tpm: numberOrUndef(e.target.value) })
              }
            />
          </Field>
          <Field label="Max inputs / request" htmlFor="voyage-max-inputs">
            <Input
              id="voyage-max-inputs"
              type="number"
              min={0}
              placeholder="128"
              value={value.max_inputs_per_request ?? ''}
              onChange={(e) =>
                onChange({ ...value, max_inputs_per_request: numberOrUndef(e.target.value) })
              }
            />
          </Field>
          <Field label="Max tokens / request" htmlFor="voyage-max-tokens">
            <Input
              id="voyage-max-tokens"
              type="number"
              min={0}
              placeholder="100000"
              value={value.max_tokens_per_request ?? ''}
              onChange={(e) =>
                onChange({ ...value, max_tokens_per_request: numberOrUndef(e.target.value) })
              }
            />
          </Field>
        </div>
        <p className="cix-hint mt-3">
          empty RPM/TPM = no self-throttling (429s on overflow); empty per-request fields fall back
          to 128 inputs / ~100K tokens
        </p>
      </div>

      <SwitchRow
        id="voyage-truncation"
        checked={value.truncation}
        onCheckedChange={(c) => onChange({ ...value, truncation: c })}
        label="Truncate over-length inputs server-side"
        hint="on: Voyage truncates a chunk past the model's context and still returns a vector, losing the tail. off: Voyage returns 400 so you can shrink the chunk size or move to a bigger context. Unrelated to the 120K-per-batch cap, which the adaptive bisect handles."
      />

      <Field
        label="API key env var"
        htmlFor="voyage-key-env"
        hint="The dashboard stores only the variable name, never the key."
      >
        <Input
          id="voyage-key-env"
          value={value.api_key_env}
          onChange={(e) => onChange({ ...value, api_key_env: e.target.value })}
          placeholder="CIX_VOYAGE_API_KEY"
          invalid={apiKeyMissing}
        />
      </Field>

      {apiKeyMissing ? (
        <Callout variant="danger">
          <b>That env var is not set on the server</b>
          <p>
            Export <Chip>{value.api_key_env}</Chip> and restart the container before saving —
            every Voyage call would fail until the key exists.
          </p>
        </Callout>
      ) : null}
    </div>
  );
}
