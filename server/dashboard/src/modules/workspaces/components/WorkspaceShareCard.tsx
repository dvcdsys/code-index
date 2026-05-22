import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { useGroups } from '@/modules/groups/hooks';
import { ShareToGroupCard } from '@/modules/groups/components/ShareToGroupCard';
import {
  useShareWorkspace,
  useUnshareWorkspace,
  useWorkspaceShares,
} from '@/modules/groups/shareHooks';

// Shown to the workspace owner / admins. GET /groups is member-scoped on the
// server, so the candidate list is exactly the groups the caller may share to
// (their own for a user, all for an admin).
export function WorkspaceShareCard({ workspaceId }: { workspaceId: string }) {
  const groups = useGroups();
  const shares = useWorkspaceShares(workspaceId);
  const share = useShareWorkspace();
  const unshare = useUnshareWorkspace();

  function onError(action: string) {
    return (err: unknown) =>
      toast.error(`Failed to ${action}`, {
        description: err instanceof ApiError ? err.detail : String(err),
      });
  }

  return (
    <ShareToGroupCard
      heading="Share with view-groups"
      description="Members of a group you share this workspace with can open it and search the projects inside that they are allowed to see."
      groups={groups.data?.groups ?? []}
      sharedIds={shares.data?.group_ids ?? []}
      busy={share.isPending || unshare.isPending}
      emptyText="You don't belong to any view-groups yet. Ask an admin to add you to one."
      onShare={(groupId) =>
        share.mutate({ id: workspaceId, groupId }, { onError: onError('share workspace') })
      }
      onUnshare={(groupId) =>
        unshare.mutate({ id: workspaceId, groupId }, { onError: onError('unshare workspace') })
      }
    />
  );
}
