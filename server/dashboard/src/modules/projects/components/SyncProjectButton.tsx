import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, type ButtonProps } from '@/ui/button';
import { useReindexProject } from '../hooks';

// SyncProjectButton triggers a SOFT update: the server fetches the latest
// commits and runs an incremental index over just the tree.Diff change set
// (no indexed_sha clear). This is the cheap, common path — for a from-scratch
// rebuild use the Reindex button. External projects only.
//
// variant/size/className are forwarded to the underlying Button so callers can
// render it prominently (project detail header) or compact (project card).
export function SyncProjectButton({
  hash,
  hostPath,
  variant = 'default',
  size = 'sm',
  className,
}: {
  hash: string;
  hostPath: string;
  variant?: ButtonProps['variant'];
  size?: ButtonProps['size'];
  className?: string;
}) {
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
      variant={variant}
      size={size}
      className={className}
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
