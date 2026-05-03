import { useState, type FormEvent } from 'react';
import { Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { useAuth } from '@/auth/useAuth';
import { useChangePassword } from '../hooks';

// Settings-page password change. Server invalidates sibling sessions and
// keeps the current cookie alive — but to make the cookie consistent with
// the new password we still log out + bounce to /login afterwards.
export function ChangePasswordForm() {
  const { logout } = useAuth();
  const change = useChangePassword();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (next !== confirm) {
      setError('New password and confirmation must match.');
      return;
    }
    if (next.length < 8) {
      setError('New password must be at least 8 characters.');
      return;
    }
    try {
      await change.mutateAsync({ current_password: current, new_password: next });
      toast.success('Password updated', {
        description: 'Please sign in again with your new password.',
      });
      await logout();
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : 'Unexpected error. Try again.';
      setError(detail);
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="space-y-1.5">
          <Label htmlFor="set-current">Current password</Label>
          <Input
            id="set-current"
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            disabled={change.isPending}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="set-next">New password</Label>
          <Input
            id="set-next"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            value={next}
            onChange={(e) => setNext(e.target.value)}
            disabled={change.isPending}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="set-confirm">Confirm new password</Label>
          <Input
            id="set-confirm"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={change.isPending}
          />
        </div>
      </div>
      {error ? (
        <Alert variant="destructive">
          <AlertTitle>Could not update password</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      ) : null}
      <div>
        <Button type="submit" disabled={change.isPending}>
          {change.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
          Update password
        </Button>
      </div>
    </form>
  );
}
