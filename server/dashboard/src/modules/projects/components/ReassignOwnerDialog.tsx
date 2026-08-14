import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Field } from '@/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { useUsers } from '@/modules/users/hooks';
import { useReassignProjectOwner } from '@/modules/groups/shareHooks';

// Admin-only, LOCAL projects only — external projects are ownerless and the
// server rejects the call with 422.
export function ReassignOwnerDialog({
  hash,
  currentOwnerId,
}: {
  hash: string;
  currentOwnerId?: string | null;
}) {
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
      toast.error('Could not reassign the owner', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">Reassign owner</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Reassign project owner</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Transfers this local project to another user. They get full control; the previous
            owner loses access unless they are an admin.
          </DialogDescription>
          <Field label="New owner">
            <Select value={selected} onValueChange={setSelected}>
              <SelectTrigger aria-label="New owner">
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
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={reassign.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={onSubmit} disabled={!selected || reassign.isPending}>
            {reassign.isPending ? <Dots /> : null}
            Reassign
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
