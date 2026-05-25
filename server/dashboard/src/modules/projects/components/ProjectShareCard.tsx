import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { useGroups } from '@/modules/groups/hooks';
import { ShareToGroupCard } from '@/modules/groups/components/ShareToGroupCard';
import { useProjectShares, useShareProject, useUnshareProject } from '@/modules/groups/shareHooks';

// Admin-only card on the external-project detail page: share the project to
// view-groups so their members can read/search it.
export function ProjectShareCard({ hash }: { hash: string }) {
  const groups = useGroups();
  const shares = useProjectShares(hash);
  const share = useShareProject();
  const unshare = useUnshareProject();

  function onError(action: string) {
    return (err: unknown) =>
      toast.error(`Failed to ${action}`, {
        description: err instanceof ApiError ? err.detail : String(err),
      });
  }

  return (
    <ShareToGroupCard
      heading="Share with view-groups"
      description="Members of a group you share this external project with can read and search it."
      groups={groups.data?.groups ?? []}
      sharedIds={shares.data?.group_ids ?? []}
      busy={share.isPending || unshare.isPending}
      emptyText="No view-groups yet — create one under View Groups."
      onShare={(groupId) =>
        share.mutate({ hash, groupId }, { onError: onError('share project') })
      }
      onUnshare={(groupId) =>
        unshare.mutate({ hash, groupId }, { onError: onError('unshare project') })
      }
    />
  );
}
