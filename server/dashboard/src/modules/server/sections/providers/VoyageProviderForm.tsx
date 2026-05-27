import { AlertTriangle, ExternalLink, Info } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { Switch } from '@/ui/switch';
import type {
  DocumentedEmbeddingLimits,
  DocumentedModelLimit,
  EmbeddingProviderSecretEnv,
} from '@/api/types';

// VoyageConfig mirrors the voyage provider's persisted config blob
// shape (see server/internal/embeddings/provider/voyage/voyage.go).
export interface VoyageConfig {
  model: string;
  api_key_env: string;
  output_dimension: number;
  output_dtype: 'float' | 'int8';
  truncation: boolean;
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
  // documentedLimits is the hardcoded snapshot of Voyage's published
  // rate limits sourced from their public docs (Voyage has no API
  // endpoint to fetch limits). Undefined when the server is older
  // than the limits-table feature; the form silently degrades.
  documentedLimits?: DocumentedEmbeddingLimits;
}

// formatNumber prints a number in thousands shorthand: 3000000 → "3M",
// 16000000 → "16M", 2000 → "2K". Used to keep the limits card compact.
function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(n % 1_000 === 0 ? 0 : 1)}K`;
  return String(n);
}

// ActiveModelLimitsCard renders the documented rate-limit row for the
// model currently selected in the form. The whole table lives behind a
// "Show all models" details expander so the form stays compact by
// default.
function ActiveModelLimitsCard({
  limits,
  selectedModel,
}: {
  limits: DocumentedEmbeddingLimits;
  selectedModel: string;
}) {
  const active = limits.models.find((m) => m.model === selectedModel);
  return (
    <div className="rounded-md border bg-muted/30 p-3 text-xs">
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">
            Documented rate limits for <code>{selectedModel}</code>{' '}
            <span className="font-normal text-muted-foreground">(Tier 1 baseline)</span>
          </div>
          {active ? (
            <LimitsRow l={active} />
          ) : (
            <div className="mt-1 text-muted-foreground">
              No entry for this model in our snapshot — consult the dashboard.
            </div>
          )}
        </div>
        <a
          href="https://dashboard.voyageai.com/"
          target="_blank"
          rel="noreferrer noopener"
          className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs hover:bg-accent"
          title="Voyage doesn't expose limits via API — open the dashboard to check your live tier"
        >
          Open Voyage dashboard <ExternalLink className="h-3 w-3" />
        </a>
      </div>

      {/* Tier multiplier note. Voyage scales limits with lifetime spend:
          ≥$100 → Tier 2 (×2), ≥$1000 → Tier 3 (×3). We can't detect
          the operator's tier (no API), so we just state the rule and
          let them mentally apply the multiplier. */}
      <div className="mt-2 text-muted-foreground">
        Multiply by <strong className="text-foreground">×2</strong> at Tier 2
        (≥&nbsp;$100 paid lifetime) and <strong className="text-foreground">×3</strong> at
        Tier 3 (≥&nbsp;$1000). Voyage doesn't expose your current tier via
        API — check the dashboard to confirm.
      </div>
      <details className="mt-3 group">
        <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
          Show all models in this snapshot
        </summary>
        <table className="mt-2 w-full text-xs">
          <thead>
            <tr className="text-left text-muted-foreground">
              <th className="pr-3 font-normal">Model</th>
              <th className="pr-3 font-normal">RPM (T1)</th>
              <th className="pr-3 font-normal">TPM (T1)</th>
              <th className="pr-3 font-normal">Inputs/req</th>
              <th className="pr-3 font-normal">Tokens/req</th>
            </tr>
          </thead>
          <tbody>
            {limits.models.map((m) => (
              <tr key={m.model} className={m.model === selectedModel ? 'font-medium' : ''}>
                <td className="pr-3"><code>{m.model}</code></td>
                <td className="pr-3">{formatNumber(m.tier1_rpm)}</td>
                <td className="pr-3">{formatNumber(m.tier1_tpm)}</td>
                <td className="pr-3">{m.max_inputs_per_request}</td>
                <td className="pr-3">{formatNumber(m.max_tokens_per_request)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
      <p className="mt-3 text-[10px] text-muted-foreground">{limits.source}</p>
    </div>
  );
}

function LimitsRow({ l }: { l: DocumentedModelLimit }) {
  return (
    <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-foreground sm:grid-cols-4">
      <div>
        <div className="text-muted-foreground">RPM (Tier 1)</div>
        <div>{formatNumber(l.tier1_rpm)}</div>
      </div>
      <div>
        <div className="text-muted-foreground">TPM (Tier 1)</div>
        <div>{formatNumber(l.tier1_tpm)}</div>
      </div>
      <div>
        <div className="text-muted-foreground">Inputs / req</div>
        <div>{l.max_inputs_per_request}</div>
      </div>
      <div>
        <div className="text-muted-foreground">Tokens / req</div>
        <div>{formatNumber(l.max_tokens_per_request)}</div>
      </div>
      {l.notes ? (
        <div className="col-span-full text-muted-foreground">{l.notes}</div>
      ) : null}
    </div>
  );
}

const MODELS = [
  'voyage-code-3',
  'voyage-3-large',
  'voyage-3',
  'voyage-3-lite',
  'voyage-code-2',
];

const DIMENSIONS = [256, 512, 1024, 2048];

export function VoyageProviderForm({ value, onChange, secretEnvs, documentedLimits }: Props) {
  const apiKeyEnv = secretEnvs.find((e) => e.name === value.api_key_env);
  const apiKeyMissing = apiKeyEnv != null && !apiKeyEnv.set;

  return (
    <div className="space-y-4">
      <Alert>
        <Info className="h-4 w-4" />
        <AlertTitle>Rate limits</AlertTitle>
        <AlertDescription>
          Voyage's free tier is capped at <strong>3 requests/minute</strong> and
          10K tokens/minute — usable for a smoke test, but the indexer will
          burst past it on any real repo and start returning 429s. For real
          usage{' '}
          <a
            href="https://dashboard.voyageai.com/"
            target="_blank"
            rel="noreferrer noopener"
            className="underline"
          >
            add a payment method
          </a>{' '}
          on the Voyage dashboard. On the free tier you can still index by
          setting <strong>concurrency = 1</strong> in the Throughput card
          below and accepting roughly 3 files/minute throughput.
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

      {documentedLimits ? (
        <ActiveModelLimitsCard limits={documentedLimits} selectedModel={value.model} />
      ) : null}

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
