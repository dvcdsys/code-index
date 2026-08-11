import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots, type ButtonProps } from '@/ui/button';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { useReindexProject } from '../hooks';

// ReindexProjectButton triggers a FULL rebuild: the server clears indexed_sha
// first, so the next job re-embeds every file and wipes the prior chunks,
// symbols and refs. Heavy and easy to fire by accident, hence the confirm.
// The cheap "pull + incremental" path is the Sync button.
export function ReindexProjectButton({
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
      toast.error('Could not enqueue the reindex', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          variant={variant}
          size={size}
          className={className}
          title="Clear the index and re-embed every file. For a quick pull + incremental update, use Sync."
        >
          Reindex
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Run a full reindex?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            The index for <span className="font-mono text-ink">{hostPath}</span> is cleared and
            every file is re-embedded from scratch — heavy, and it can take a while. For a quick
            pull plus incremental update, use Sync instead.
          </DialogDescription>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reindex.isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={reindex.isPending}>
            {reindex.isPending ? <Dots /> : null}
            Reindex
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
