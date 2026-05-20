import { useEffect, useState } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { RadioGroup, RadioGroupItem } from '@/ui/radio-group';
import { Skeleton } from '@/ui/skeleton';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { useProjectGitRepo, useUpdateProjectSync, type GitRepo, type SyncMethod } from '../hooks';

const METHODS: ReadonlyArray<{ value: SyncMethod; label: string; hint: string }> = [
  {
    value: 'webhook',
    label: 'Webhook (push-driven)',
    hint: 'GitHub notifies the server on every push. Needs a public URL and a token with admin:repo_hook. Falls back to polling if the hook can’t be installed.',
  },
  {
    value: 'polling',
    label: 'Polling',
    hint: 'The server fetches on an interval and reindexes when the branch moves. Works without admin rights on the repo.',
  },
  {
    value: 'manual',
    label: 'Manual only',
    hint: 'No automatic sync. Use the Reindex button (or the API) to update on demand.',
  },
];

function deriveMethod(g: GitRepo): SyncMethod {
  if (g.webhook_mode === 'auto' || g.webhook_mode === 'manual') return 'webhook';
  if (g.polling_enabled) return 'polling';
  return 'manual';
}

const DEFAULT_INTERVAL_SEC = 300;

// SyncSettingsCard lets an operator reconfigure how an external project is kept
// in sync, directly from the project page. Read-only for non-admins.
export function SyncSettingsCard({ hash, isAdmin }: { hash: string; isAdmin: boolean }) {
  const gitRepo = useProjectGitRepo(hash, true);
  const update = useUpdateProjectSync();
  const data = gitRepo.data;

  const [method, setMethod] = useState<SyncMethod | null>(null);
  const [intervalSec, setIntervalSec] = useState<string>('');

  useEffect(() => {
    if (data) {
      setMethod(deriveMethod(data));
      setIntervalSec(String(data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC));
    }
  }, [data]);

  if (gitRepo.isLoading) {
    return <Skeleton className="h-56 w-full" />;
  }
  if (gitRepo.error || !data) {
    // 404 for local projects — the caller already gates on external, so any
    // error here is unexpected; render nothing rather than a scary alert.
    return null;
  }

  const selected = method ?? deriveMethod(data);

  async function save() {
    const secs = Number(intervalSec);
    try {
      const res = await update.mutateAsync({
        hash,
        sync_method: selected,
        poll_interval_seconds:
          selected === 'polling' && Number.isFinite(secs) && secs > 0 ? secs : undefined,
      });
      if (res.note) {
        toast.info('Sync updated', { description: res.note });
      } else {
        toast.success('Sync settings saved');
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to update sync settings', { description: detail });
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Sync settings</CardTitle>
        <CardDescription>
          Choose how this GitHub project is reindexed when it changes upstream.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <RadioGroup
          value={selected}
          onValueChange={(v) => setMethod(v as SyncMethod)}
          className="space-y-3"
          disabled={!isAdmin || update.isPending}
        >
          {METHODS.map((m) => (
            <div key={m.value} className="flex items-start gap-3">
              <RadioGroupItem id={`sync-${m.value}`} value={m.value} className="mt-0.5" />
              <div className="space-y-0.5">
                <Label htmlFor={`sync-${m.value}`} className="font-medium">
                  {m.label}
                </Label>
                <p className="text-xs text-muted-foreground">{m.hint}</p>
              </div>
            </div>
          ))}
        </RadioGroup>

        {selected === 'polling' && (
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1">
              <Label htmlFor="poll-interval" className="text-xs">
                Poll interval (seconds)
              </Label>
              <Input
                id="poll-interval"
                type="number"
                min={1}
                value={intervalSec}
                onChange={(e) => setIntervalSec(e.target.value)}
                disabled={!isAdmin || update.isPending}
                className="w-40"
              />
            </div>
            <p className="text-xs text-muted-foreground">
              Measured from the end of the last index run. Empty / 0 uses the
              server default; values below the configured floor are clamped up.
            </p>
          </div>
        )}

        <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
          <dt className="text-muted-foreground">Webhook mode</dt>
          <dd className="font-mono">{data.webhook_mode}</dd>
          <dt className="text-muted-foreground">Polling</dt>
          <dd>
            {data.polling_enabled
              ? `every ${data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC}s`
              : 'off'}
          </dd>
          {data.polling_enabled && data.next_poll_at && (
            <>
              <dt className="text-muted-foreground">Next poll</dt>
              <dd title={formatDateTime(data.next_poll_at)}>{formatRelative(data.next_poll_at)}</dd>
            </>
          )}
          {data.last_error && (
            <>
              <dt className="text-destructive">Last error</dt>
              <dd className="break-all text-destructive">{data.last_error}</dd>
            </>
          )}
        </dl>

        {isAdmin ? (
          <div className="flex items-center gap-3">
            <Button size="sm" onClick={() => void save()} disabled={update.isPending}>
              {update.isPending ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="mr-1 h-4 w-4" />
              )}
              Save sync settings
            </Button>
            {selected !== deriveMethod(data) && (
              <span className="text-xs text-muted-foreground">Unsaved change</span>
            )}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">Changing sync settings requires an admin.</p>
        )}
      </CardContent>
    </Card>
  );
}
