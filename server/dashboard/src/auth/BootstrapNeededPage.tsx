import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';

// Shown when /auth/bootstrap-status reports `needs_bootstrap: true` —
// i.e. the database has no users and the operator hasn't supplied the
// bootstrap admin env vars. There's nothing the visitor can do from the
// browser; this page exists to explain what to set on the server.
export default function BootstrapNeededPage() {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-muted/20 px-4">
      <div className="w-full max-w-xl space-y-6">
        <div className="text-center">
          <div className="text-2xl font-semibold tracking-tight">Server not configured</div>
          <div className="mt-1 text-sm text-muted-foreground">
            cix-server has no users yet. An administrator must seed the first account before the dashboard becomes available.
          </div>
        </div>

        <Alert>
          <AlertTitle>How to bootstrap the first admin</AlertTitle>
          <AlertDescription>
            <p className="mb-3">
              Restart the server with both of these environment variables set:
            </p>
            <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs leading-relaxed">
{`CIX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \\
CIX_BOOTSTRAP_ADMIN_PASSWORD='change-me-on-first-login' \\
./cix-server`}
            </pre>
            <p className="mt-3">
              On first login the admin will be required to change the
              password. After that, both env vars are ignored on subsequent
              starts.
            </p>
          </AlertDescription>
        </Alert>
      </div>
    </div>
  );
}
