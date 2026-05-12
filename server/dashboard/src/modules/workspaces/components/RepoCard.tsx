import { useEffect, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Link2,
  Loader2,
  RefreshCw,
  Trash2,
  Webhook,
  WebhookOff,
} from 'lucide-react';
import { api } from '@/api/client';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardContent } from '@/ui/card';
import { formatRelative } from '@/lib/formatDate';
import type { WorkspaceRepo } from '../types';
import { isInFlight } from '../types';

// RepoCard renders one workspace_repo as a self-contained card. Status
// drives the visual treatment — a spinner with an elapsed-time counter
// is shown while clone/index is in flight so the operator gets feedback
// without staring at a stale page.
export function RepoCard({
  repo,
  onDeleted,
  onReindexed,
}: {
  repo: WorkspaceRepo;
  onDeleted: () => void;
  onReindexed: () => void;
}) {
  const [busy, setBusy] = useState<'delete' | 'reindex' | null>(null);
  const inFlight = isInFlight(repo.status);

  async function handleDelete() {
    const detachMsg = repo.is_linked
      ? `Detach the linked project "${repo.github_url}@${repo.branch}" from this workspace?\n\nThe project itself stays — only this workspace's link to it is removed.`
      : `Detach "${repo.github_url}@${repo.branch}" from this workspace?\n\nThe indexed project will also be removed.`;
    if (!confirm(detachMsg)) {
      return;
    }
    setBusy('delete');
    try {
      await api.delete<void>(`/workspaces/${repo.workspace_id}/repos/${repo.id}`);
      onDeleted();
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function handleReindex() {
    setBusy('reindex');
    try {
      await api.post(
        `/workspaces/${repo.workspace_id}/repos/${repo.id}/reindex`,
        {},
      );
      onReindexed();
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card
      className={
        repo.status === 'failed'
          ? 'border-destructive/60'
          : inFlight
          ? 'border-amber-500/40'
          : ''
      }
    >
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium">
              {repo.github_url}
              <span className="text-muted-foreground"> @ {repo.branch}</span>
            </div>
            <div
              className="mt-0.5 truncate text-xs text-muted-foreground"
              title={repo.project_path}
            >
              {repo.project_path}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {/* Reindex is a no-op for linked rows — they don't own the
                clone, so the canonical workspace must trigger it. Hide
                the button to make that contract obvious. */}
            {!repo.is_linked && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy !== null || inFlight}
                title="Reindex"
                onClick={handleReindex}
              >
                <RefreshCw className={`size-4 ${busy === 'reindex' ? 'animate-spin' : ''}`} />
              </Button>
            )}
            <Button
              variant="ghost"
              size="sm"
              disabled={busy !== null}
              title={repo.is_linked ? 'Detach linked project' : 'Detach repo'}
              onClick={handleDelete}
            >
              <Trash2 className="size-4" />
            </Button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <StatusBadge repo={repo} />
          {repo.is_linked ? (
            <Badge variant="outline" className="gap-1 font-normal">
              <Link2 className="size-3" /> linked
            </Badge>
          ) : (
            <WebhookBadge repo={repo} />
          )}
          {repo.last_indexed_at && (
            <span className="text-muted-foreground">
              · indexed {formatRelative(repo.last_indexed_at)}
            </span>
          )}
        </div>

        {repo.last_error && (
          <div className="rounded bg-destructive/10 px-2 py-1 text-xs text-destructive">
            {repo.last_error}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// StatusBadge renders the colour-coded status + an elapsed-time read
// while a clone/index job is running. The elapsed counter ticks once a
// second so the user can tell the job hasn't silently stalled.
function StatusBadge({ repo }: { repo: WorkspaceRepo }) {
  const inFlight = isInFlight(repo.status);
  const elapsed = useElapsedSince(inFlight ? repo.updated_at : null);

  if (repo.status === 'indexed') {
    return (
      <Badge className="gap-1">
        <CheckCircle2 className="size-3" /> indexed
      </Badge>
    );
  }
  if (repo.status === 'failed') {
    return (
      <Badge variant="destructive" className="gap-1">
        <AlertTriangle className="size-3" /> failed
      </Badge>
    );
  }
  return (
    <Badge variant="secondary" className="gap-1">
      <Loader2 className="size-3 animate-spin" />
      {repo.status}
      {elapsed !== null && (
        <span className="font-mono text-muted-foreground">· {formatDuration(elapsed)}</span>
      )}
    </Badge>
  );
}

function WebhookBadge({ repo }: { repo: WorkspaceRepo }) {
  switch (repo.webhook_mode) {
    case 'auto':
      return (
        <Badge variant="outline" className="gap-1 font-normal">
          <Webhook className="size-3" /> auto
        </Badge>
      );
    case 'manual':
      return (
        <Badge variant="outline" className="gap-1 font-normal">
          <Webhook className="size-3" /> manual
        </Badge>
      );
    case 'disabled':
      return (
        <Badge variant="outline" className="gap-1 font-normal text-muted-foreground">
          <WebhookOff className="size-3" /> disabled
        </Badge>
      );
  }
}

// useElapsedSince ticks once a second so the in-flight badge shows
// elapsed time without re-fetching from the server.
function useElapsedSince(iso: string | null): number | null {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (iso === null) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [iso]);
  if (iso === null) return null;
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return null;
  return Math.max(0, Math.floor((now - ts) / 1000));
}

function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}m ${s}s`;
}
