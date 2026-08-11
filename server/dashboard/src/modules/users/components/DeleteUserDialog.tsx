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
import { Field, Input } from '@/ui/input';
import { useDeleteUser } from '../hooks';

// Typing the email is the second factor: in a list of similar-looking rows a
// single misplaced click should not delete an account. The server cascades
// sessions and API keys.
export function DeleteUserDialog({ userId, email }: { userId: string; email: string }) {
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
      toast.error('Could not delete the user', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
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
        <Button variant="quietDanger" size="sm">
          Delete
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <span className="cix-dot is-busy" aria-hidden />
          <DialogTitle>Delete this user?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Permanently removes <span className="font-mono text-ink">{email}</span> along with
            every session and API key they own. This cannot be undone.
          </DialogDescription>
          <Field label="Type the email to confirm" htmlFor="delete-confirm-email">
            <Input
              id="delete-confirm-email"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={email}
              autoComplete="off"
            />
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={del.isPending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={onConfirm}
            disabled={del.isPending || typed.trim() !== email}
          >
            {del.isPending ? <Dots /> : null}
            Delete user
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
