import { useState } from 'react';
import { Loader2, UserCog } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
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
import { useUsers } from '@/modules/users/hooks';
import { useReassignProjectOwner } from '@/modules/groups/shareHooks';

// Admin-only: reassign the owner of a LOCAL project. External projects are
// ownerless (the server rejects them with 422).
export function ReassignOwnerDialog({ hash, currentOwnerId }: { hash: string; currentOwnerId?: string | null }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState('');
  const users = useUsers();
  const reassign = useReassignProjectOwner();

  async function onSubmit() {
    if (!selected) return;
    try {
      await reassign.mutateAsync({ hash, ownerUserId: selected });
      toast.success('Owner reassigned');
      setOpen(false);
      setSelected('');
    } catch (err) {
      toast.error('Failed to reassign owner', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <UserCog className="mr-1 h-4 w-4" />
          Reassign owner
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Reassign project owner</DialogTitle>
          <DialogDescription>
            Transfer this local project to another user. They will have full
            control; the previous owner loses access unless they are an admin.
          </DialogDescription>
        </DialogHeader>
        <Select value={selected} onValueChange={setSelected}>
          <SelectTrigger>
            <SelectValue placeholder="Select a user…" />
          </SelectTrigger>
          <SelectContent>
            {(users.data?.users ?? [])
              .filter((u) => u.id !== currentOwnerId && !u.disabled)
              .map((u) => (
                <SelectItem key={u.id} value={u.id}>
                  {u.email}
                </SelectItem>
              ))}
          </SelectContent>
        </Select>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reassign.isPending}>
            Cancel
          </Button>
          <Button onClick={onSubmit} disabled={!selected || reassign.isPending}>
            {reassign.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Reassign
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
