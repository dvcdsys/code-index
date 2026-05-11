import { useEffect, useState } from 'react';
import { AlertCircle, Boxes, Plus, Trash2 } from 'lucide-react';
import { ApiError, api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';

type Workspace = {
  id: string;
  name: string;
  description: string;
  created_at: string;
  updated_at: string;
};

type WorkspaceListResponse = {
  workspaces: Workspace[];
  total: number;
};

// WorkspacesPage is the PR1 skeleton. It supports the full CRUD path so the
// API surface gets exercised end-to-end, but the rich UI (per-repo views,
// status panels, two-stage search) lands in later PRs of the workspaces
// feature branch.
export default function WorkspacesPage() {
  const [list, setList] = useState<Workspace[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [featureOff, setFeatureOff] = useState(false);

  async function reload() {
    try {
      const resp = await api.get<WorkspaceListResponse>('/workspaces');
      setList(resp.workspaces);
      setError(null);
      setFeatureOff(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) {
        setFeatureOff(true);
        setList([]);
        return;
      }
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    void reload();
  }, []);

  if (featureOff) {
    return (
      <div className="space-y-6">
        <Header />
        <Alert>
          <AlertCircle className="size-4" />
          <AlertTitle>Workspaces feature is disabled</AlertTitle>
          <AlertDescription>
            Set <code>CIX_WORKSPACES_ENABLED=true</code> and restart the
            server to enable cross-project workspaces. PR1 ships CRUD only;
            repository attachment, webhooks, and two-stage search land in
            subsequent releases.
          </AlertDescription>
        </Alert>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <Header onCreated={reload} />
      {error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Could not load workspaces</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {list === null ? (
        <div className="space-y-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : list.length === 0 ? (
        <EmptyState />
      ) : (
        <ul className="divide-y rounded-md border">
          {list.map((ws) => (
            <WorkspaceRow key={ws.id} ws={ws} onDeleted={reload} />
          ))}
        </ul>
      )}
    </div>
  );
}

function Header({ onCreated }: { onCreated?: () => void }) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Workspaces</h1>
        <p className="text-sm text-muted-foreground">
          Group GitHub repositories for cross-project semantic search.
        </p>
      </div>
      {onCreated && <CreateWorkspaceDialog onCreated={onCreated} />}
    </header>
  );
}

function EmptyState() {
  return (
    <div className="rounded-md border bg-muted/30 p-8 text-center">
      <Boxes className="mx-auto mb-3 size-8 text-muted-foreground" />
      <p className="text-sm font-medium">No workspaces yet</p>
      <p className="mt-1 text-xs text-muted-foreground">
        Create one to start grouping repositories. Repository attachment and
        cross-project search arrive in later releases.
      </p>
    </div>
  );
}

function WorkspaceRow({ ws, onDeleted }: { ws: Workspace; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false);

  async function handleDelete() {
    if (!confirm(`Delete workspace "${ws.name}"?`)) return;
    setBusy(true);
    try {
      await api.delete<void>(`/workspaces/${ws.id}`);
      onDeleted();
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <div className="min-w-0">
        <div className="truncate font-medium">{ws.name}</div>
        {ws.description && (
          <div className="truncate text-xs text-muted-foreground">{ws.description}</div>
        )}
      </div>
      <Button variant="ghost" size="sm" disabled={busy} onClick={handleDelete}>
        <Trash2 className="size-4" />
      </Button>
    </li>
  );
}

function CreateWorkspaceDialog({ onCreated }: { onCreated: () => void }) {
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
        <Button>
          <Plus className="mr-1 size-4" />
          New workspace
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create workspace</DialogTitle>
          <DialogDescription>
            Workspaces group GitHub repositories. Repository attachment lands
            in a later release; for now you can just claim a name.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1">
            <Label htmlFor="ws-name">Name</Label>
            <Input
              id="ws-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="platform"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="ws-desc">Description (optional)</Label>
            <Input
              id="ws-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="microservices cluster"
            />
          </div>
          {err && (
            <Alert variant="destructive">
              <AlertDescription>{err}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={busy || name.trim() === ''}>
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
