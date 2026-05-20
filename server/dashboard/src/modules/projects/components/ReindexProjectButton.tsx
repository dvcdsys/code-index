import type { MouseEvent } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useReindexProject } from '../hooks';

// ReindexProjectButton — the default click triggers an incremental
// reindex (server picks based on git tree.Diff between indexed_sha and
// new HEAD). Shift+click triggers a full reindex via ?full=true — the
// server clears indexed_sha first so the next job runs the full
// embedding pipeline. Use Shift+click as the manual recovery path when
// index drift is suspected and the auto-detected model-change full
// reindex isn't enough.
export function ReindexProjectButton({ hash, hostPath }: { hash: string; hostPath: string }) {
  const reindex = useReindexProject();

  async function trigger(full: boolean) {
    try {
      const res = await reindex.mutateAsync({ hash, full });
      const label = full ? 'Full reindex' : 'Reindex';
      if (res.status === 'already_running') {
        toast.info(`${label} already running`, { description: hostPath });
      } else {
        toast.success(`${label} enqueued`, {
          description: full ? `${hostPath} — full rebuild` : hostPath,
        });
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to enqueue reindex', { description: detail });
    }
  }

  function onClick(e: MouseEvent<HTMLButtonElement>) {
    void trigger(e.shiftKey);
  }

  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      disabled={reindex.isPending}
      title="Click for incremental reindex (git diff). Shift+Click for full reindex (rebuild everything)."
    >
      {reindex.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <RefreshCw className="mr-1 h-4 w-4" />
      )}
      Reindex
    </Button>
  );
}
