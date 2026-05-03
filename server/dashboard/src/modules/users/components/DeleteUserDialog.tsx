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
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { useDeleteUser } from '../hooks';

// Two-factor confirm: typing the email avoids accidental destructive clicks
// in a list of similar-looking rows. Server cascade-deletes sessions + keys.
export function DeleteUserDialog({
  userId,
  email,
}: {
  userId: string;
  email: string;
}) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState('');
  const del = useDeleteUser();

  function reset() {
    setTyped('');
    del.reset();
  }

  async function onConfirm() {
    if (typed.trim() !== email) return;
    try {
      await del.mutateAsync(userId);
      toast.success('User deleted', { description: email });
      setOpen(false);
      reset();
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to delete user', { description: detail });
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
        >
          <Trash2 className="mr-1 h-4 w-4" />
          Delete
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete user?</DialogTitle>
          <DialogDescription>
            Permanently removes <span className="font-mono text-foreground">{email}</span>{' '}
            and cascades all their sessions and API keys. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="delete-confirm-email">
            Type the email to confirm
          </Label>
          <Input
            id="delete-confirm-email"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={email}
            autoComplete="off"
          />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={del.isPending}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={onConfirm}
            disabled={del.isPending || typed.trim() !== email}
          >
            {del.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Delete user
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
