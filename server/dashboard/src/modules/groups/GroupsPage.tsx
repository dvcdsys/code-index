import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Card, Empty } from '@/ui/card';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';
import { Page } from '@/ui/page';
import { SkeletonRows } from '@/ui/skeleton';
import { CreateGroupDialog } from './components/CreateGroupDialog';
import { GroupMembersDialog } from './components/GroupMembersDialog';
import { useDeleteGroup, useGroups } from './hooks';

export default function GroupsPage() {
  const { data, error, isLoading } = useGroups();
  const del = useDeleteGroup();
  // A native confirm() would be the only browser-chrome dialog left in the
  // app, and deleting a group drops every membership and share it carries —
  // it gets the same modal treatment as the other destructive actions.
  const [pendingDelete, setPendingDelete] = useState<{ id: string; name: string } | null>(null);

  useStatusFact(data ? `${data.total} group${data.total === 1 ? '' : 's'}` : null);

  async function onDelete() {
    if (!pendingDelete) return;
    try {
      await del.mutateAsync(pendingDelete.id);
      toast.success('Group deleted', { description: pendingDelete.name });
      setPendingDelete(null);
    } catch (err) {
      toast.error('Could not delete the group', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Page
      title="View groups"
      subtitle="A group is a set of users. Share external projects and workspaces to a group to grant its members read and search access."
      action={<CreateGroupDialog />}
    >
      {isLoading ? (
        <SkeletonRows rows={3} />
      ) : error ? (
        <Callout variant="danger">
          <b>Could not load groups</b>
          <p>{error instanceof ApiError ? error.detail : String(error)}</p>
        </Callout>
      ) : !data || data.groups.length === 0 ? (
        <Empty title="No view groups yet">
          Create a group, add users to it, then share external projects and workspaces to the
          group so its members can search them.
        </Empty>
      ) : (
        <Card>
          {data.groups.map((g) => (
            <div key={g.id} className="cix-row">
              <div className="min-w-0 flex-1">
                <div className="cix-row__title truncate">{g.name}</div>
                {g.description ? (
                  <div className="cix-row__meta truncate">{g.description}</div>
                ) : null}
              </div>
              <GroupMembersDialog group={g} />
              <Button
                variant="quietDanger"
                size="sm"
                onClick={() => setPendingDelete({ id: g.id, name: g.name })}
                disabled={del.isPending}
              >
                Delete
              </Button>
            </div>
          ))}
        </Card>
      )}

      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(next) => (!next && !del.isPending ? setPendingDelete(null) : null)}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <span className="cix-dot is-busy" aria-hidden />
            <DialogTitle>Delete this group?</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              <span className="font-mono text-ink">{pendingDelete?.name}</span> loses every
              membership and every share pointing at it. Members keep their accounts; they just
              stop seeing what was shared here.
            </DialogDescription>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPendingDelete(null)} disabled={del.isPending}>
              Cancel
            </Button>
            <Button variant="danger" onClick={onDelete} disabled={del.isPending}>
              {del.isPending ? <Dots /> : null}
              Delete group
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  );
}
