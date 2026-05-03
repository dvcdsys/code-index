import { useState } from 'react';
import { Loader2, UserPlus } from 'lucide-react';
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import type { Role } from '@/api/types';
import { useCreateUser } from '../hooks';

// Invite-only flow: admin sets the initial password and shares it out-of-band.
// The new user is forced to change it on first login (server sets
// must_change_password=true). Field minimums (≥8 chars) mirror the server.
export function InviteUserDialog() {
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<Role>('viewer');
  const [pw, setPw] = useState('');
  const create = useCreateUser();

  function reset() {
    setEmail('');
    setRole('viewer');
    setPw('');
    create.reset();
  }

  async function onSubmit() {
    const trimmedEmail = email.trim();
    if (!trimmedEmail || pw.length < 8) return;
    try {
      await create.mutateAsync({
        email: trimmedEmail,
        role,
        initial_password: pw,
      });
      toast.success('User created', {
        description: `Share the initial password with ${trimmedEmail}. They will be required to change it on first login.`,
      });
      setOpen(false);
      reset();
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to invite user', { description: detail });
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
        <Button>
          <UserPlus className="mr-1 h-4 w-4" />
          Invite user
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Invite user</DialogTitle>
          <DialogDescription>
            Creates a user with an initial password you set. The user is forced
            to change it on first login.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="invite-email">Email</Label>
            <Input
              id="invite-email"
              type="email"
              autoFocus
              autoComplete="off"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="user@example.com"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="invite-role">Role</Label>
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger id="invite-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="viewer">Viewer (read-only)</SelectItem>
                <SelectItem value="admin">Admin (full access)</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="invite-pw">Initial password</Label>
            <Input
              id="invite-pw"
              type="text"
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
              The user&rsquo;s account will be flagged{' '}
              <code className="rounded bg-muted px-1">must_change_password</code>;
              they cannot use the system until they pick a new password.
            </AlertDescription>
          </Alert>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            onClick={onSubmit}
            disabled={create.isPending || !email.trim() || pw.length < 8}
          >
            {create.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Create user
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
