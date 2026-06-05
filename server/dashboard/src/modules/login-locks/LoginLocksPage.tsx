import { AlertCircle, ShieldCheck } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Skeleton } from '@/ui/skeleton';
import { LoginLocksTable } from './components/LoginLocksTable';
import { useLoginLocks } from './hooks';

export default function LoginLocksPage() {
  const { data, error, isLoading } = useLoginLocks();

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Login security</h1>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Accounts and source IPs currently blocked by the login rate limiter
            after too many failed attempts. Locks clear themselves as their
            window expires — reset one to let a user back in immediately.
          </p>
        </div>
      </header>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Failed to load login locks</AlertTitle>
          <AlertDescription>
            {error instanceof ApiError ? error.detail : String(error)}
          </AlertDescription>
        </Alert>
      ) : !data || data.locks.length === 0 ? (
        <EmptyState />
      ) : (
        <LoginLocksTable locks={data.locks} />
      )}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <ShieldCheck className="h-10 w-10 text-muted-foreground" />
      <p className="text-base font-medium">No active login locks</p>
      <p className="max-w-sm text-sm text-muted-foreground">
        Nobody is currently rate-limited. Locks appear here automatically when
        an account or IP exceeds the failed-login threshold.
      </p>
    </div>
  );
}
