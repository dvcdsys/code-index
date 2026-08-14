import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Chip } from '@/ui/code';
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
import { useResetUserPassword } from '../hooks';

// Mirrors the invite flow: the admin sets a temporary password and shares it
// out of band. The server flags must_change_password and revokes the user's
// sessions, so their next login behaves like a first login.
export function ResetPasswordDialog({ userId, email }: { userId: string; email: string }) {
  const [open, setOpen] = useState(false);
  const [pw, setPw] = useState('');
  const reset = useResetUserPassword();

  function clear() {
    setPw('');
    reset.reset();
  }

  async function onSubmit() {
    if (pw.length < 8) return;
    try {
      await reset.mutateAsync({ id: userId, body: { new_password: pw } });
      toast.success('Password reset', {
        description: `Share the temporary password with ${email} — they must change it on next login.`,
      });
      setOpen(false);
      clear();
    } catch (err) {
      toast.error('Could not reset the password', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) clear();
      }}
    >
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" title="Set a temporary password for this user">
          Reset password
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Reset password</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Sets a temporary password for <span className="font-mono text-ink">{email}</span>.
            Their active sessions are signed out and they must change it on the next login.
          </DialogDescription>

          <Field
            label="Temporary password"
            htmlFor="reset-pw"
            hint="At least 8 characters. Shown once — share it over your team's secure channel."
          >
            <Input
              id="reset-pw"
              type="text"
              autoFocus
              autoComplete="new-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
            />
          </Field>

          <Callout variant="warn">
            <p>
              The account is flagged <Chip>must_change_password</Chip> and its sessions revoked.
            </p>
          </Callout>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reset.isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onSubmit} disabled={reset.isPending || pw.length < 8}>
            {reset.isPending ? <Dots /> : null}
            Reset password
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
