import { AlertCircle, Trash2, UsersRound } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import { CreateGroupDialog } from './components/CreateGroupDialog';
import { GroupMembersDialog } from './components/GroupMembersDialog';
import { useDeleteGroup, useGroups } from './hooks';

export default function GroupsPage() {
  const { data, error, isLoading } = useGroups();
  const del = useDeleteGroup();

  async function onDelete(id: string, name: string) {
    if (!window.confirm(`Delete group "${name}"? This removes all its memberships and shares.`)) {
      return;
    }
    try {
      await del.mutateAsync(id);
      toast.success('Group deleted');
    } catch (err) {
      toast.error('Failed to delete group', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">View Groups</h1>
          <p className="text-sm text-muted-foreground">
            {data ? `${data.total} ${data.total === 1 ? 'group' : 'groups'}` : ' '}
          </p>
        </div>
        <CreateGroupDialog />
      </header>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Failed to load groups</AlertTitle>
          <AlertDescription>
            {error instanceof ApiError ? error.detail : String(error)}
          </AlertDescription>
        </Alert>
      ) : !data || data.groups.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="space-y-2">
          {data.groups.map((g) => (
            <div
              key={g.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3"
            >
              <div className="min-w-0">
                <div className="font-medium">{g.name}</div>
                {g.description ? (
                  <div className="truncate text-sm text-muted-foreground">{g.description}</div>
                ) : null}
              </div>
              <div className="flex items-center gap-2">
                <GroupMembersDialog group={g} />
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => void onDelete(g.id, g.name)}
                  disabled={del.isPending}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <UsersRound className="h-10 w-10 text-muted-foreground" />
      <p className="text-base font-medium">No view-groups yet</p>
      <p className="max-w-sm text-sm text-muted-foreground">
        Create a group, add users to it, then share external projects and
        workspaces to the group so its members can search them.
      </p>
    </div>
  );
}
