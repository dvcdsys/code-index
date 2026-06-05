import { useState } from 'react';
import { KeyRound, Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription } from '@/ui/alert';
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
import { useResetUserPassword } from '../hooks';

// Admin-initiated reset. Mirrors the invite flow: the admin sets a temporary
// password and shares it out-of-band. The server flags the user
// must_change_password=true and revokes their sessions, so on next login they
// are forced to pick a new password (same as first login). Field minimum
// (≥8 chars) mirrors the server.
export function ResetPasswordDialog({
  userId,
  email,
}: {
  userId: string;
  email: string;
}) {
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
        description: `Share the temporary password with ${email}. They will be required to change it on next login.`,
      });
      setOpen(false);
      clear();
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to reset password', { description: detail });
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
        <Button variant="ghost" size="sm" title="Reset this user's password">
          <KeyRound className="mr-1 h-4 w-4" />
          Reset password
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Reset password</DialogTitle>
          <DialogDescription>
            Sets a temporary password for {email}. They are forced to change it
            on next login and any active sessions are signed out.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="reset-pw">Temporary password</Label>
            <Input
              id="reset-pw"
              type="text"
              autoFocus
              autoComplete="new-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              placeholder="At least 8 characters"
            />
            <p className="text-xs text-muted-foreground">
              Shown once. Share via your team&rsquo;s preferred secure channel.
            </p>
          </div>
          <Alert>
            <AlertDescription className="text-xs">
              The account will be flagged{' '}
              <code className="rounded bg-muted px-1">must_change_password</code>{' '}
              and its sessions revoked; the user cannot continue until they pick
              a new password.
            </AlertDescription>
          </Alert>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reset.isPending}>
            Cancel
          </Button>
          <Button onClick={onSubmit} disabled={reset.isPending || pw.length < 8}>
            {reset.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Reset password
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
