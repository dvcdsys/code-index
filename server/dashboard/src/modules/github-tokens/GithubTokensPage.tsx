import { useEffect, useState } from 'react';
import { AlertCircle, Github, Plus, Trash2 } from 'lucide-react';
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

type GithubToken = {
  id: string;
  name: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string | null;
};

type GithubTokenListResponse = {
  tokens: GithubToken[];
  total: number;
};

// GithubTokensPage manages encrypted-at-rest GitHub PATs used by the
// workspaces feature for cloning private repos and (optionally) registering
// webhooks. The plaintext value is sent on POST and never returned —
// subsequent operations identify tokens by id.
export default function GithubTokensPage() {
  const [list, setList] = useState<GithubToken[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [featureOff, setFeatureOff] = useState(false);

  async function reload() {
    try {
      const resp = await api.get<GithubTokenListResponse>('/github-tokens');
      setList(resp.tokens);
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
            GitHub tokens are part of the workspaces feature. Set{' '}
            <code>CIX_WORKSPACES_ENABLED=true</code> and restart the server
            to enable.
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
          <AlertTitle>Could not load tokens</AlertTitle>
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
          {list.map((t) => (
            <TokenRow key={t.id} token={t} onDeleted={reload} />
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
        <h1 className="text-2xl font-semibold tracking-tight">GitHub Tokens</h1>
        <p className="text-sm text-muted-foreground">
          Personal Access Tokens for cloning private repositories. Stored
          encrypted; the plaintext value is never returned after creation.
        </p>
      </div>
      {onCreated && <CreateTokenDialog onCreated={onCreated} />}
    </header>
  );
}

function EmptyState() {
  return (
    <div className="rounded-md border bg-muted/30 p-8 text-center">
      <Github className="mx-auto mb-3 size-8 text-muted-foreground" />
      <p className="text-sm font-medium">No GitHub tokens yet</p>
      <p className="mt-1 text-xs text-muted-foreground">
        Tokens are required when adding private repositories to a workspace.
      </p>
    </div>
  );
}

function TokenRow({ token, onDeleted }: { token: GithubToken; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false);

  async function handleDelete() {
    if (!confirm(`Delete token "${token.name}"? This cannot be undone.`)) return;
    setBusy(true);
    try {
      await api.delete<void>(`/github-tokens/${token.id}`);
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
        <div className="truncate font-medium">{token.name}</div>
        <div className="truncate text-xs text-muted-foreground">
          scopes: {token.scopes.length ? token.scopes.join(', ') : '—'}
          {token.last_used_at && (
            <> · last used {new Date(token.last_used_at).toLocaleString()}</>
          )}
        </div>
      </div>
      <Button variant="ghost" size="sm" disabled={busy} onClick={handleDelete}>
        <Trash2 className="size-4" />
      </Button>
    </li>
  );
}

function CreateTokenDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [token, setToken] = useState('');
  const [scopes, setScopes] = useState('repo');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      const scopeList = scopes
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      await api.post('/github-tokens', { name, token, scopes: scopeList });
      setName('');
      setToken('');
      setScopes('repo');
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
          Add token
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add GitHub token</DialogTitle>
          <DialogDescription>
            Stored encrypted-at-rest with AES-256-GCM. The plaintext value
            never leaves this request — there is no way to retrieve it after
            saving.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1">
            <Label htmlFor="tok-name">Name</Label>
            <Input
              id="tok-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="personal"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="tok-value">Token value</Label>
            <Input
              id="tok-value"
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="ghp_..."
              className="font-mono"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="tok-scopes">Scopes (comma-separated)</Label>
            <Input
              id="tok-scopes"
              value={scopes}
              onChange={(e) => setScopes(e.target.value)}
              placeholder="repo, admin:repo_hook"
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
          <Button
            onClick={submit}
            disabled={busy || name.trim() === '' || token.trim() === ''}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
