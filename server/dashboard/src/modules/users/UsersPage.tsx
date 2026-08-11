import { ApiError } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Empty } from '@/ui/card';
import { Page } from '@/ui/page';
import { SkeletonRows } from '@/ui/skeleton';
import { InviteUserDialog } from './components/InviteUserDialog';
import { UsersTable } from './components/UsersTable';
import { useUsers } from './hooks';

export default function UsersPage() {
  const { user } = useAuth();
  const { data, error, isLoading } = useUsers();

  useStatusFact(data ? `${data.total} user${data.total === 1 ? '' : 's'}` : null);

  return (
    <Page
      title="Users"
      subtitle="Accounts with dashboard access. Roles, per-user indexing permission, sessions and API-key counts."
      action={<InviteUserDialog />}
    >
      {isLoading ? (
        <SkeletonRows rows={4} />
      ) : error ? (
        <Callout variant="danger">
          <b>Could not load users</b>
          <p>{error instanceof ApiError ? error.detail : String(error)}</p>
        </Callout>
      ) : !data || data.users.length === 0 ? (
        <Empty title="No users yet">
          Invite the first teammate to share dashboard access. If you can read this, your own
          account is already in the list.
        </Empty>
      ) : (
        <UsersTable users={data.users} currentUserId={user?.id} />
      )}
    </Page>
  );
}
