import { useState } from 'react';
import { Hammer, Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, type ButtonProps } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { useReindexProject } from '../hooks';

// ReindexProjectButton triggers a FULL rebuild — the server clears
// indexed_sha first, so the next job re-embeds every file (wipes prior
// chunks/symbols/refs). This is the heavy "rebuild from scratch" path;
// for the cheap "pull + incremental" path use the Sync button instead.
// Gated behind a confirmation dialog because it's an expensive operation
// that's easy to trigger by accident.
//
// variant/size/className are forwarded to the trigger Button so callers can
// render it prominently (project detail header) or compact (projects list
// row / card), mirroring SyncProjectButton.
export function ReindexProjectButton({
  hash,
  hostPath,
  variant = 'outline',
  size = 'sm',
  className,
}: {
  hash: string;
  hostPath: string;
  variant?: ButtonProps['variant'];
  size?: ButtonProps['size'];
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const reindex = useReindexProject();

  async function onConfirm() {
    try {
      const res = await reindex.mutateAsync({ hash, full: true });
      if (res.status === 'already_running') {
        toast.info('Reindex already running', { description: hostPath });
      } else {
        toast.success('Full reindex enqueued', { description: `${hostPath} — full rebuild` });
      }
      setOpen(false);
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to enqueue reindex', { description: detail });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          variant={variant}
          size={size}
          className={className}
          title="Full reindex — clears the index and re-embeds every file. For a quick pull + incremental update, use Sync."
        >
          <Hammer className="mr-1 h-4 w-4" />
          Reindex
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Run a full reindex?</DialogTitle>
          <DialogDescription>
            This clears the index for{' '}
            <span className="font-mono text-foreground">{hostPath}</span> and re-embeds every
            file from scratch — a heavy operation that can take a while. For a quick pull +
            incremental update, use Sync instead.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reindex.isPending}>
            Cancel
          </Button>
          <Button variant="default" onClick={onConfirm} disabled={reindex.isPending}>
            {reindex.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Reindex
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
