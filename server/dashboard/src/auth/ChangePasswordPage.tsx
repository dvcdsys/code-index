import { useState, type FormEvent } from 'react';
import { ApiError, api } from '@/api/client';
import type { ChangePasswordRequest } from '@/api/types';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { toast } from '@/ui/sonner';
import { useAuth } from './useAuth';

// Forced password-change page — reached either right after a bootstrap
// admin first logs in, or after an admin invite. Server-side: a successful
// POST /auth/change-password ALSO revokes every other session for this
// user, so we log out and bounce back to /login on success.
export default function ChangePasswordPage() {
  const { logout } = useAuth();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
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
    setSubmitting(true);
    try {
      const req: ChangePasswordRequest = { current_password: current, new_password: next };
      await api.post('/auth/change-password', req);
      toast.success('Password updated. Please sign in with your new password.');
      // Server already invalidated this session — calling logout cleans up
      // the cookie + clears cached /me so App.tsx falls back to LoginPage.
      await logout();
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : 'Unexpected error. Try again.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="flex min-h-dvh items-center justify-center bg-muted/20 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="text-2xl font-semibold tracking-tight">Change your password</div>
          <div className="mt-1 text-sm text-muted-foreground">
            For security, you must set a new password before continuing.
          </div>
        </div>

        <form onSubmit={onSubmit} className="space-y-4 rounded-lg border bg-background p-6 shadow-sm">
          <div className="space-y-1.5">
            <Label htmlFor="current">Current password</Label>
            <Input
              id="current"
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              disabled={submitting}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="next">New password</Label>
            <Input
              id="next"
              type="password"
              autoComplete="new-password"
              required
              minLength={8}
              value={next}
              onChange={(e) => setNext(e.target.value)}
              disabled={submitting}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="confirm">Confirm new password</Label>
            <Input
              id="confirm"
              type="password"
              autoComplete="new-password"
              required
              minLength={8}
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              disabled={submitting}
            />
          </div>

          {error && (
            <Alert variant="destructive">
              <AlertTitle>Could not update password</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <Button type="submit" className="w-full" disabled={submitting}>
            {submitting ? 'Updating…' : 'Update password'}
          </Button>
        </form>
      </div>
    </div>
  );
}
