import { useState, type FormEvent } from 'react';
import { ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Field, Input } from '@/ui/input';
import { AuthShell } from './AuthShell';
import { useAuth } from './useAuth';

// Standalone login. Lives outside the Shell so no sidebar peeks through; on
// success the auth state flips and App routes to /change-password or /.
export default function LoginPage() {
  const { login } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    // Read the credentials off the form, not out of state. A password manager
    // can fill both fields without React ever seeing an input event, and then
    // the state copy is empty while the form is visibly filled.
    const data = new FormData(e.currentTarget);
    const submittedEmail = String(data.get('email') ?? '').trim() || email;
    const submittedPassword = String(data.get('password') ?? '') || password;
    setError(null);
    setSubmitting(true);
    try {
      await login({ email: submittedEmail, password: submittedPassword });
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : 'Could not reach the server. Try again.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle="Operator access to this cix server."
      footer="CLI and CI authenticate with API keys, not with this form."
    >
      <form onSubmit={onSubmit} className="flex flex-col gap-4">
        <Field label="Email" htmlFor="email">
          <Input
            id="email"
            name="email"
            type="email"
            autoComplete="username"
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={submitting}
          />
        </Field>

        <Field label="Password" htmlFor="password">
          <Input
            id="password"
            name="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
          />
        </Field>

        {error && (
          <Callout variant="danger">
            <b>Sign-in failed</b>
            <p>{error}</p>
          </Callout>
        )}

        {/* Disabled only while the request is in flight. Gating it on the two
            state values also gates Enter: a browser skips implicit submission
            when the form's default button is disabled, so an autofilled form
            (state empty, fields visibly full) had a dead Enter key. `required`
            on both inputs is what catches an actually-empty submit. */}
        <Button type="submit" variant="primary" className="mt-1 w-full" disabled={submitting}>
          {submitting ? (
            <>
              <Dots /> Signing in
            </>
          ) : (
            'Sign in'
          )}
        </Button>
      </form>
    </AuthShell>
  );
}
