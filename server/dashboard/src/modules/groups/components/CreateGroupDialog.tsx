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
import { Field, Input } from '@/ui/input';
import { useCreateGroup } from '../hooks';

// Admin-only. External projects and workspaces are later shared TO a group,
// which is what grants its members read/search access.
export function CreateGroupDialog() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const create = useCreateGroup();

  function reset() {
    setName('');
    setDescription('');
    create.reset();
  }

  async function onSubmit() {
    const trimmed = name.trim();
    if (!trimmed) return;
    try {
      await create.mutateAsync({ name: trimmed, description: description.trim() });
      toast.success('Group created', { description: trimmed });
      setOpen(false);
      reset();
    } catch (err) {
      toast.error('Could not create the group', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary">New group</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Create a view group</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            A group is a set of users. Share external projects and workspaces to it to grant the
            members read and search access.
          </DialogDescription>
          <Field label="Name" htmlFor="group-name">
            <Input
              id="group-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="product-agents"
            />
          </Field>
          <Field label="Description" htmlFor="group-desc" hint="Optional.">
            <Input
              id="group-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={onSubmit} disabled={create.isPending || !name.trim()}>
            {create.isPending ? <Dots /> : null}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
