import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { Group } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { useUsers } from '@/modules/users/hooks';
import { useAddGroupMember, useGroupMembers, useRemoveGroupMember } from '../hooks';

// Admin-only: who belongs to a view group. Members get read/search access to
// everything shared to it.
export function GroupMembersDialog({ group }: { group: Group }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState('');
  const members = useGroupMembers(group.id, open);
  const users = useUsers();
  const addMember = useAddGroupMember();
  const removeMember = useRemoveGroupMember();

  const memberIds = new Set((members.data?.members ?? []).map((m) => m.user_id));
  const candidates = (users.data?.users ?? []).filter((u) => !memberIds.has(u.id));

  async function add() {
    if (!selected) return;
    try {
      await addMember.mutateAsync({ id: group.id, userId: selected });
      setSelected('');
    } catch (err) {
      toast.error('Could not add the member', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  async function remove(userId: string) {
    try {
      await removeMember.mutateAsync({ id: group.id, userId });
    } catch (err) {
      toast.error('Could not remove the member', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  const list = members.data?.members ?? [];

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">Members</Button>
      </DialogTrigger>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{group.name}</DialogTitle>
          <span className="ml-auto font-mono text-[11.5px] font-normal text-muted">
            {list.length} member{list.length === 1 ? '' : 's'}
          </span>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Members can read and search every external project and workspace shared to this group.
          </DialogDescription>

          <div className="flex items-end gap-2">
            <Select value={selected} onValueChange={setSelected}>
              <SelectTrigger className="flex-1" aria-label="Add a user">
                <SelectValue
                  placeholder={candidates.length ? 'Add a user…' : 'Every user is a member'}
                />
              </SelectTrigger>
              <SelectContent>
                {candidates.map((u) => (
                  <SelectItem key={u.id} value={u.id}>
                    {u.email}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button variant="primary" onClick={add} disabled={!selected || addMember.isPending}>
              {addMember.isPending ? <Dots /> : null}
              Add
            </Button>
          </div>

          <div className="max-h-64 overflow-y-auto border">
            {members.isLoading ? (
              <p className="m-0 px-3.5 py-3 font-mono text-[12px] text-muted">loading…</p>
            ) : list.length === 0 ? (
              <p className="m-0 px-3.5 py-3 text-sm text-dim">No members yet.</p>
            ) : (
              list.map((m) => (
                <div key={m.user_id} className="cix-row py-2.5">
                  <span className="min-w-0 flex-1 truncate text-sm">{m.email}</span>
                  <Badge variant="quiet">{m.role}</Badge>
                  <Button
                    variant="quietDanger"
                    size="sm"
                    onClick={() => void remove(m.user_id)}
                    disabled={removeMember.isPending}
                  >
                    Remove
                  </Button>
                </div>
              ))
            )}
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
