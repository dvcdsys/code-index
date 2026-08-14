import { Callout } from '@/ui/alert';
import { CodeBlock } from '@/ui/code';
import { AuthShell } from './AuthShell';

const BOOTSTRAP_CMD = [
  'CIX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \\',
  "CIX_BOOTSTRAP_ADMIN_PASSWORD='change-me-on-first-login' \\",
  './cix-server',
].join('\n');

// Shown when /auth/bootstrap-status reports `needs_bootstrap: true` — the
// database has no users and the operator hasn't supplied the bootstrap admin
// env vars. Nothing here is actionable from the browser, so the page is a
// single copyable command plus what happens after it runs.
export default function BootstrapNeededPage() {
  return (
    <AuthShell
      wide
      title="Server not configured"
      subtitle="This cix-server has no users yet. The first admin is seeded from the environment, not from the browser."
    >
      <div className="flex flex-col gap-4">
        <span className="cix-label">Restart the server with</span>
        <CodeBlock command={BOOTSTRAP_CMD} wrap />
        <Callout>
          <b>What happens next</b>
          <p>
            The admin is created on boot and must change the password at first sign-in. Both
            variables are ignored on every subsequent start, so it is safe to leave them in place.
          </p>
        </Callout>
      </div>
    </AuthShell>
  );
}
