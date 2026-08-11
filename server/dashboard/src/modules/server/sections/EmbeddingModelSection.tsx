import { useEffect, useId, useState } from 'react';
import type { ModelEntry, RuntimeConfig } from '@/api/types';
import { Callout } from '@/ui/alert';
import { Card, CardBody, CardHead } from '@/ui/card';
import { Chip } from '@/ui/code';
import { Field, Input } from '@/ui/input';
import { RadioCard, RadioGroup } from '@/ui/radio-group';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
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
  // POSIX-only is enough — the server runs on Linux/macOS. Windows would need
  // an extra drive-letter test.
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

// Exactly one model source, chosen with two radio panels:
//   repo — from cix's own GGUF cache, or a repo ID to download on first use
//   path — an absolute .gguf already on this host
// Switching modes resets the draft so the parent never holds a half-typed
// cross-mode value.
export function EmbeddingModelSection({ config, draftModel, onDraftChange }: Props) {
  const repoInputId = useId();
  const pathInputId = useId();

  const [mode, setMode] = useState<Mode>(() => (isAbsPath(draftModel) ? 'path' : 'repo'));

  // Follow the draft when it changes from outside (initial fetch, refresh
  // after save) — otherwise the radio lies about what is selected.
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
    // repo → restore a sensible default so an absolute path doesn't linger
    // under a disabled field. path → clear, so the save button stays disabled
    // until a real path is typed.
    onDraftChange(next === 'repo' ? (config?.recommended?.embedding_model ?? '') : '');
  }

  return (
    <Card>
      <CardHead title="Embedding model" aside={<SourcePill source={config?.source?.embedding_model} />}>
        <span className="ml-2 font-mono text-[11px] font-normal text-dim">
          restart on change
        </span>
      </CardHead>
      <CardBody className="flex flex-col gap-5">
        <RadioGroup
          value={mode}
          onValueChange={(v) => switchTo(v as Mode)}
          className="grid gap-3 sm:grid-cols-2"
        >
          <RadioCard
            id="model-mode-repo"
            value="repo"
            selected={mode === 'repo'}
            title="Hugging Face"
            hint={config?.recommended?.embedding_model ?? 'owner/repo'}
          />
          <RadioCard
            id="model-mode-path"
            value="path"
            selected={mode === 'path'}
            title="Local file"
            hint="/models/*.gguf"
          />
        </RadioGroup>

        {mode === 'repo' ? (
          <div className="flex flex-col gap-4">
            {models.isLoading ? (
              <Skeleton className="h-[38px]" />
            ) : cached.length > 0 ? (
              <Field
                label="Cached repos"
                hint={cacheDir ? `scanned ${cacheDir}` : undefined}
              >
                <Select
                  value={matched ? matched.id : ''}
                  onValueChange={(v) => v && onDraftChange(v)}
                >
                  <SelectTrigger aria-label="Cached GGUF repositories">
                    <SelectValue placeholder="Type a repo ID below" />
                  </SelectTrigger>
                  <SelectContent>
                    {cached.map((m) => (
                      <SelectItem key={m.path} value={m.id}>
                        {m.id} ({formatSize(m.size_bytes)})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            ) : (
              <Callout variant="warn">
                <b>No cached repositories</b>
                <p>
                  {cacheDir ? (
                    <>
                      Nothing under <Chip>{cacheDir}</Chip>. Type a repo ID below — the first save
                      downloads it into the cache.
                    </>
                  ) : (
                    <>
                      No cache directory reported. Type a repo ID below, or pick “Local file” if
                      the model lives outside cix.
                    </>
                  )}
                </p>
              </Callout>
            )}

            <Field
              label="Repo ID"
              htmlFor={repoInputId}
              hint={
                config?.recommended
                  ? `recommended ${config.recommended.embedding_model}`
                  : 'owner/repo'
              }
            >
              <Input
                id={repoInputId}
                value={draftModel}
                onChange={(e) => onDraftChange(e.target.value)}
                placeholder="awhiteside/CodeRankEmbed-Q8_0-GGUF"
              />
            </Field>
          </div>
        ) : (
          <Field
            label="Absolute path to .gguf"
            htmlFor={pathInputId}
            hint="Must be readable by the cix-server process. Used as-is — not copied into the cache."
          >
            <Input
              id={pathInputId}
              value={draftModel}
              onChange={(e) => onDraftChange(e.target.value)}
              placeholder="/models/coderankembed-q8_0.gguf"
            />
          </Field>
        )}
      </CardBody>
    </Card>
  );
}
