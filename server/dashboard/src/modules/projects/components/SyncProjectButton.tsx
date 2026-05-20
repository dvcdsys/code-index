import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useReindexProject } from '../hooks';

// SyncProjectButton triggers a SOFT update: the server fetches the latest
// commits and runs an incremental index over just the tree.Diff change set
// (no indexed_sha clear). This is the cheap, common path — for a from-scratch
// rebuild use the Reindex button. External projects only.
export function SyncProjectButton({ hash, hostPath }: { hash: string; hostPath: string }) {
  const sync = useReindexProject();

  async function onClick() {
    try {
      const res = await sync.mutateAsync({ hash, full: false });
      if (res.status === 'already_running') {
        toast.info('Sync already running', { description: hostPath });
      } else {
        toast.success('Sync enqueued', { description: `${hostPath} — pull + incremental` });
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to enqueue sync', { description: detail });
    }
  }

  return (
    <Button
      variant="default"
      size="sm"
      onClick={onClick}
      disabled={sync.isPending}
      title="Sync — pull the latest commits and incrementally index the changed files."
    >
      {sync.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <RefreshCw className="mr-1 h-4 w-4" />
      )}
      Sync
    </Button>
  );
}
