import { AlertTriangle } from 'lucide-react';
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

export function VoyageProviderForm({ value, onChange, secretEnvs }: Props) {
  const apiKeyEnv = secretEnvs.find((e) => e.name === value.api_key_env);
  const apiKeyMissing = apiKeyEnv != null && !apiKeyEnv.set;

  return (
    <div className="space-y-4">
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
