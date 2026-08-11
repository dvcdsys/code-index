import { useState } from 'react';
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
      toast.error('Could not revoke the key', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" className="text-accent hover:bg-danger-bg">
          Revoke
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <span className="cix-dot is-busy" aria-hidden />
          <DialogTitle>Revoke this API key?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Every client presenting <span className="font-mono text-ink">{prefix}…</span> ({name})
            starts getting 401 immediately. The audit row stays in the database, but the key can
            never authenticate again.
          </DialogDescription>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={revoke.isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={revoke.isPending}>
            {revoke.isPending ? <Dots /> : null}
            Revoke key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
