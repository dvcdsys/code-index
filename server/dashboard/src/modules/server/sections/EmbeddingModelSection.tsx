import { useEffect, useId, useState } from 'react';
import { AlertTriangle } from 'lucide-react';
import type { ModelEntry, RuntimeConfig } from '@/api/types';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { RadioGroup, RadioGroupItem } from '@/ui/radio-group';
import { Skeleton } from '@/ui/skeleton';
import { useGGUFModels } from '../hooks';
import { SourcePill } from '../components/SourcePill';

interface Props {
  config?: RuntimeConfig;
  draftModel: string;
  onDraftChange: (next: string) => void;
}

type Mode = 'repo' | 'path';

function isAbsPath(v: string): boolean {
  // POSIX-only check is enough — the server is Linux/macOS for the
  // foreseeable future. Windows path support would need an additional
  // drive-letter test (`/^[a-zA-Z]:[\\/]/.test(v)`).
  return v.startsWith('/');
}

function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

// EmbeddingModelSection lets the admin pick exactly one model source:
//   1. "HuggingFace repo" — selects from cix's own GGUF cache (or types
//      a repo ID for first-use download). Active = repo mode.
//   2. "Local file path" — points at an absolute .gguf path on the host.
//      Active = path mode.
//
// The two inputs are mutually exclusive. Switching modes resets the draft
// to a sensible default for the new mode (recommended repo / empty path)
// so the parent component never holds a half-typed cross-mode value.
export function EmbeddingModelSection({ config, draftModel, onDraftChange }: Props) {
  const repoSelectId = useId();
  const repoInputId = useId();
  const pathInputId = useId();

  const [mode, setMode] = useState<Mode>(() => (isAbsPath(draftModel) ? 'path' : 'repo'));

  // Sync mode if the draft is changed from the outside (initial fetch,
  // optimistic refresh after save). Without this the radio would lie
  // after the parent updates draftModel from a server-reload.
  useEffect(() => {
    setMode(isAbsPath(draftModel) ? 'path' : 'repo');
  }, [draftModel]);

  const models = useGGUFModels();
  const cached: ModelEntry[] = models.data?.models ?? [];
  const cacheDir = models.data?.cache_dir ?? '';
  const matched = cached.find((m) => m.id === draftModel);

  function switchTo(next: Mode) {
    if (next === mode) return;
    setMode(next);
    if (next === 'repo') {
      // Switching out of path → restore a sensible repo default so the
      // form doesn't show an absolute path under the disabled path input.
      onDraftChange(config?.recommended?.embedding_model ?? '');
    } else {
      // Switching into path → clear the field so the user types a fresh
      // absolute path. Empty string is invalid, save button stays disabled
      // until they enter something.
      onDraftChange('');
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Embedding model
          <SourcePill source={config?.source?.embedding_model} />
        </CardTitle>
        <CardDescription>
          Pick one source. Saving triggers a sidecar restart so the new
          weights load before any further embedding.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <RadioGroup
          value={mode}
          onValueChange={(v) => switchTo(v as Mode)}
          className="grid grid-cols-1 gap-2 sm:grid-cols-2"
        >
          <ModeOption
            value="repo"
            label="HuggingFace repo"
            hint="Selects from cix's own GGUF cache. First use downloads from HuggingFace."
          />
          <ModeOption
            value="path"
            label="Local file path"
            hint="Use an absolute path to a .gguf file already on this host."
          />
        </RadioGroup>

        {/* Repo mode: dropdown when cache has entries, free-text input either way. */}
        <fieldset
          className={`space-y-3 ${mode === 'repo' ? '' : 'pointer-events-none opacity-50'}`}
          aria-disabled={mode !== 'repo'}
        >
          {models.isLoading ? (
            <Skeleton className="h-9 w-full" />
          ) : cached.length > 0 ? (
            <div className="space-y-1.5">
              <Label htmlFor={repoSelectId}>Cached repos</Label>
              <select
                id={repoSelectId}
                value={matched ? matched.id : ''}
                onChange={(e) => onDraftChange(e.target.value)}
                disabled={mode !== 'repo'}
                className="block w-full rounded-md border bg-background px-3 py-2 text-sm disabled:cursor-not-allowed"
              >
                <option value="">— Type a repo ID below —</option>
                {cached.map((m) => (
                  <option key={m.path} value={m.id}>
                    {m.id} ({formatSize(m.size_bytes)})
                  </option>
                ))}
              </select>
              {cacheDir ? (
                <p className="text-xs text-muted-foreground">
                  Scanned <code>{cacheDir}</code>
                </p>
              ) : null}
            </div>
          ) : (
            <Alert>
              <AlertTriangle className="h-4 w-4" />
              <AlertTitle>No cached repos</AlertTitle>
              <AlertDescription>
                {cacheDir
                  ? <>Nothing under <code>{cacheDir}</code>. Type a repo ID below — first save will download to cache.</>
                  : <>No cache directory reported. Type a repo ID below or switch to "Local file path" if the model lives outside cix.</>}
              </AlertDescription>
            </Alert>
          )}

          <div className="space-y-1.5">
            <Label htmlFor={repoInputId}>Repo ID (owner/repo)</Label>
            <Input
              id={repoInputId}
              value={mode === 'repo' ? draftModel : ''}
              onChange={(e) => onDraftChange(e.target.value)}
              placeholder="awhiteside/CodeRankEmbed-Q8_0-GGUF"
              disabled={mode !== 'repo'}
              className="font-mono text-xs"
            />
            {config?.recommended ? (
              <p className="text-xs text-muted-foreground">
                Recommended: <code>{config.recommended.embedding_model}</code>
              </p>
            ) : null}
          </div>
        </fieldset>

        {/* Path mode: single absolute-path input. */}
        <fieldset
          className={`space-y-1.5 ${mode === 'path' ? '' : 'pointer-events-none opacity-50'}`}
          aria-disabled={mode !== 'path'}
        >
          <Label htmlFor={pathInputId}>Absolute path to .gguf file</Label>
          <Input
            id={pathInputId}
            value={mode === 'path' ? draftModel : ''}
            onChange={(e) => onDraftChange(e.target.value)}
            placeholder="/Users/me/.cache/huggingface/hub/.../coderankembed-q8_0.gguf"
            disabled={mode !== 'path'}
            className="font-mono text-xs"
          />
          <p className="text-xs text-muted-foreground">
            File must be readable by the cix-server process. The path is used as-is — cix will not copy it into its cache.
          </p>
        </fieldset>
      </CardContent>
    </Card>
  );
}

function ModeOption({ value, label, hint }: { value: Mode; label: string; hint: string }) {
  const id = useId();
  return (
    <label
      htmlFor={id}
      className="flex cursor-pointer items-start gap-3 rounded-md border p-3 hover:border-foreground/40"
    >
      <RadioGroupItem id={id} value={value} className="mt-0.5" />
      <div className="space-y-0.5">
        <div className="text-sm font-medium leading-none">{label}</div>
        <div className="text-xs text-muted-foreground">{hint}</div>
      </div>
    </label>
  );
}
