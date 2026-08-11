import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots, type ButtonProps } from '@/ui/button';
import { useReindexProject } from '../hooks';

// SyncProjectButton triggers a SOFT update: the server fetches the latest
// commits and incrementally indexes just the tree.Diff change set (no
// indexed_sha clear). The cheap, common path — for a from-scratch rebuild use
// the Reindex button. External projects only.
//
// variant/size/className forward to the Button so callers can render it
// prominently (detail header) or compact (list row).
export function SyncProjectButton({
  hash,
  hostPath,
  variant,
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
      toast.error('Could not enqueue the sync', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Button
      variant={variant}
      size={size}
      className={className}
      onClick={onClick}
      disabled={sync.isPending}
      title="Pull the latest commits and incrementally index the changed files."
    >
      {sync.isPending ? <Dots /> : null}
      Sync
    </Button>
  );
}
