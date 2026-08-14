import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots } from '@/ui/button';
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
import { useDeleteProject } from '../hooks';

export function DeleteProjectDialog({
  hash,
  hostPath,
  redirectAfter = false,
}: {
  hash: string;
  hostPath: string;
  redirectAfter?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const del = useDeleteProject();
  const navigate = useNavigate();

  async function onConfirm() {
    try {
      await del.mutateAsync(hash);
      toast.success('Project deleted', { description: hostPath });
      setOpen(false);
      if (redirectAfter) navigate('/projects');
    } catch (err) {
      toast.error('Could not delete the project', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="quietDanger" size="sm">
          Delete
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <span className="cix-dot is-busy" aria-hidden />
          <DialogTitle>Delete this project?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            The project record and every indexed chunk, symbol and reference for{' '}
            <span className="font-mono text-ink">{hostPath}</span> are removed. Files on disk are
            untouched. This cannot be undone.
          </DialogDescription>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={del.isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={del.isPending}>
            {del.isPending ? <Dots /> : null}
            Delete project
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
