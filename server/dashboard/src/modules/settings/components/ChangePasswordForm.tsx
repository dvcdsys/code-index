import { useState, type FormEvent } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Field, Input } from '@/ui/input';
import { useAuth } from '@/auth/useAuth';
import { useChangePassword } from '../hooks';

// The server invalidates sibling sessions and keeps this cookie alive, but we
// log out anyway so the credential in the browser matches the new password.
export function ChangePasswordForm() {
  const { logout } = useAuth();
  const change = useChangePassword();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
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
    try {
      await change.mutateAsync({ current_password: current, new_password: next });
      toast.success('Password updated', { description: 'Sign in again with the new one.' });
      await logout();
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : 'Unexpected error. Try again.');
    }
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Current" htmlFor="set-current">
          <Input
            id="set-current"
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            disabled={change.isPending}
          />
        </Field>
        <Field label="New" htmlFor="set-next" hint="≥ 8 characters">
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
        </Field>
        <Field
          label="Confirm"
          htmlFor="set-confirm"
          error={mismatch ? 'Does not match.' : undefined}
        >
          <Input
            id="set-confirm"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            invalid={mismatch}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={change.isPending}
          />
        </Field>
      </div>

      {error ? (
        <Callout variant="danger">
          <b>Could not update the password</b>
          <p>{error}</p>
        </Callout>
      ) : null}

      <div>
        <Button type="submit" variant="primary" disabled={change.isPending}>
          {change.isPending ? <Dots /> : null}
          Update password
        </Button>
      </div>
    </form>
  );
}
