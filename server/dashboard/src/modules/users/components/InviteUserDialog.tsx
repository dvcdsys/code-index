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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import type { Role } from '@/api/types';
import { useCreateUser } from '../hooks';

// Invite-only: the admin sets an initial password and shares it out of band.
// The server flags must_change_password, so the user has to replace it on
// first login. The ≥8 characters minimum mirrors the server.
export function InviteUserDialog() {
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<Role>('user');
  const [pw, setPw] = useState('');
  const create = useCreateUser();

  function reset() {
    setEmail('');
    setRole('user');
    setPw('');
    create.reset();
  }

  async function onSubmit() {
    const trimmed = email.trim();
    if (!trimmed || pw.length < 8) return;
    try {
      await create.mutateAsync({ email: trimmed, role, initial_password: pw });
      toast.success('User created', {
        description: `Share the initial password with ${trimmed} — they must change it on first login.`,
      });
      setOpen(false);
      reset();
    } catch (err) {
      toast.error('Could not invite the user', {
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
        <Button variant="primary">Invite user</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Invite a user</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Creates the account with an initial password you choose. The user is forced to change
            it on first login.
          </DialogDescription>

          <Field label="Email" htmlFor="invite-email">
            <Input
              id="invite-email"
              type="email"
              autoFocus
              autoComplete="off"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="user@example.com"
            />
          </Field>

          <Field label="Role" htmlFor="invite-role">
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger id="invite-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">user</SelectItem>
                <SelectItem value="admin">admin — full access</SelectItem>
              </SelectContent>
            </Select>
          </Field>

          <Field
            label="Initial password"
            htmlFor="invite-pw"
            hint="At least 8 characters. Shown once — share it over your team's secure channel."
          >
            <Input
              id="invite-pw"
              type="text"
              autoComplete="new-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
            />
          </Field>

          <Callout>
            <p>
              The account is flagged <Chip>must_change_password</Chip> and cannot be used until
              they pick a new one.
            </p>
          </Callout>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={onSubmit}
            disabled={create.isPending || !email.trim() || pw.length < 8}
          >
            {create.isPending ? <Dots /> : null}
            Create user
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
