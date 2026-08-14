import { useState } from 'react';
import { api } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import { Callout } from '@/ui/alert';
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

export function CreateWorkspaceDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      await api.post('/workspaces', { name, description });
      setName('');
      setDescription('');
      setOpen(false);
      onCreated();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="primary">New workspace</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Create a workspace</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            A workspace groups repositories so one query searches all of them. Attach the
            repositories after it exists.
          </DialogDescription>
          <Field label="Name" htmlFor="ws-name">
            <Input
              id="ws-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="platform"
            />
          </Field>
          <Field label="Description" htmlFor="ws-desc" hint="Optional.">
            <Input
              id="ws-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="microservices cluster"
            />
          </Field>
          {err ? (
            <Callout variant="danger">
              <p>{err}</p>
            </Callout>
          ) : null}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy || name.trim() === ''}>
            {busy ? <Dots /> : null}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
