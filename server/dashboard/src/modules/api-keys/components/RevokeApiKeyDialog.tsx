import { useState } from 'react';
import { Loader2, Trash2 } from 'lucide-react';
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
import { useRevokeApiKey } from '../hooks';

export function RevokeApiKeyDialog({
  id,
  name,
  prefix,
}: {
  id: string;
  name: string;
  prefix: string;
}) {
  const [open, setOpen] = useState(false);
  const revoke = useRevokeApiKey();

  async function onConfirm() {
    try {
      await revoke.mutateAsync(id);
      toast.success('API key revoked', { description: name });
      setOpen(false);
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to revoke key', { description: detail });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
        >
          <Trash2 className="mr-1 h-4 w-4" />
          Revoke
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Revoke this API key?</DialogTitle>
          <DialogDescription>
            Any client using <span className="font-mono text-foreground">{prefix}…</span> ({name})
            will start receiving 401 immediately. The audit row stays in the
            database but the key cannot authenticate again.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={revoke.isPending}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={revoke.isPending}>
            {revoke.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Revoke key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
