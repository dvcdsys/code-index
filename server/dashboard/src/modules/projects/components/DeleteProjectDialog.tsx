import { useState } from 'react';
import { Loader2, Trash2 } from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import {
  Dialog,
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
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to delete project', { description: detail });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive">
          <Trash2 className="mr-1 h-4 w-4" />
          Delete
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete this project?</DialogTitle>
          <DialogDescription>
            This removes the project record and all indexed chunks, symbols, and references for{' '}
            <span className="font-mono text-foreground">{hostPath}</span>. The files on disk are
            not touched. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={del.isPending}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={del.isPending}>
            {del.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Delete project
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
