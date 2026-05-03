import { AlertCircle, Users as UsersIcon } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Skeleton } from '@/ui/skeleton';
import { useAuth } from '@/auth/useAuth';
import { InviteUserDialog } from './components/InviteUserDialog';
import { UsersTable } from './components/UsersTable';
import { useUsers } from './hooks';

export default function UsersPage() {
  const { user } = useAuth();
  const { data, error, isLoading } = useUsers();

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Users</h1>
          <p className="text-sm text-muted-foreground">
            {data ? `${data.total} ${data.total === 1 ? 'user' : 'users'}` : ' '}
          </p>
        </div>
        <InviteUserDialog />
      </header>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Failed to load users</AlertTitle>
          <AlertDescription>
            {error instanceof ApiError ? error.detail : String(error)}
          </AlertDescription>
        </Alert>
      ) : !data || data.users.length === 0 ? (
        <EmptyState />
      ) : (
        <UsersTable users={data.users} currentUserId={user?.id} />
      )}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <UsersIcon className="h-10 w-10 text-muted-foreground" />
      <p className="text-base font-medium">No users yet</p>
      <p className="max-w-sm text-sm text-muted-foreground">
        Invite the first teammate to share dashboard access. Bootstrap admin
        always counts — if you can read this, your account is in the list.
      </p>
    </div>
  );
}
