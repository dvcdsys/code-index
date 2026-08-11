import { useState, type FormEvent } from 'react';
import { ApiError, api } from '@/api/client';
import type { ChangePasswordRequest } from '@/api/types';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Field, Input } from '@/ui/input';
import { toast } from '@/ui/sonner';
import { AuthShell } from './AuthShell';
import { useAuth } from './useAuth';

// Forced password change — reached right after a bootstrap admin's first
// login, or after an admin invite. A successful POST also revokes every other
// session for this user server-side, so we log out and bounce to /login.
export default function ChangePasswordPage() {
  const { logout } = useAuth();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const mismatch = confirm.length > 0 && next !== confirm;

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (next !== confirm) {
      setError('The new password and its confirmation must match.');
      return;
    }
    if (next.length < 8) {
      setError('The new password must be at least 8 characters.');
      return;
    }
    setSubmitting(true);
    try {
      const req: ChangePasswordRequest = { current_password: current, new_password: next };
      await api.post('/auth/change-password', req);
      toast.success('Password updated — sign in with the new one.');
      // The server already invalidated this session; logout clears the cookie
      // and the cached /me so App falls back to LoginPage.
      await logout();
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : 'Unexpected error. Try again.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Change your password"
      subtitle="A new password is required before this account can be used."
      footer="Changing the password signs out every other session for this account."
    >
      <form onSubmit={onSubmit} className="flex flex-col gap-4">
        <Field label="Current password" htmlFor="current">
          <Input
            id="current"
            type="password"
            autoComplete="current-password"
            autoFocus
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            disabled={submitting}
          />
        </Field>

        <Field label="New password" htmlFor="next" hint="At least 8 characters.">
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
        </Field>

        <Field
          label="Confirm new password"
          htmlFor="confirm"
          error={mismatch ? 'Does not match.' : undefined}
        >
          <Input
            id="confirm"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            invalid={mismatch}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={submitting}
          />
        </Field>

        {error && (
          <Callout variant="danger">
            <b>Could not update the password</b>
            <p>{error}</p>
          </Callout>
        )}

        <Button type="submit" variant="primary" className="mt-1 w-full" disabled={submitting}>
          {submitting ? (
            <>
              <Dots /> Updating
            </>
          ) : (
            'Update password'
          )}
        </Button>
      </form>
    </AuthShell>
  );
}
