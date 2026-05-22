import { useState } from 'react';
import { Loader2, Trash2, UserPlus } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import type { Group } from '@/api/types';
import { useUsers } from '@/modules/users/hooks';
import { useAddGroupMember, useGroupMembers, useRemoveGroupMember } from '../hooks';

// Admin-only: manage which users belong to a view-group. Members get
// read/search access to whatever is shared to the group.
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
      toast.error('Failed to add member', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  async function remove(userId: string) {
    try {
      await removeMember.mutateAsync({ id: group.id, userId });
    } catch (err) {
      toast.error('Failed to remove member', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <UserPlus className="mr-1 h-4 w-4" />
          Members
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{group.name} — members</DialogTitle>
          <DialogDescription>
            Members can read and search every external project and workspace
            shared to this group.
          </DialogDescription>
        </DialogHeader>

        <div className="flex items-end gap-2">
          <div className="flex-1">
            <Select value={selected} onValueChange={setSelected}>
              <SelectTrigger>
                <SelectValue placeholder={candidates.length ? 'Add a user…' : 'All users are members'} />
              </SelectTrigger>
              <SelectContent>
                {candidates.map((u) => (
                  <SelectItem key={u.id} value={u.id}>
                    {u.email}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button onClick={add} disabled={!selected || addMember.isPending}>
            {addMember.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Add
          </Button>
        </div>

        <div className="max-h-64 space-y-1 overflow-y-auto">
          {members.isLoading ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : (members.data?.members ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">No members yet.</p>
          ) : (
            members.data!.members.map((m) => (
              <div
                key={m.user_id}
                className="flex items-center justify-between rounded-md border px-3 py-2 text-sm"
              >
                <span className="truncate">
                  {m.email}
                  <span className="ml-2 text-xs capitalize text-muted-foreground">{m.role}</span>
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => void remove(m.user_id)}
                  disabled={removeMember.isPending}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </div>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
