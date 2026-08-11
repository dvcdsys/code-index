import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { ApiError, api } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Card, Empty } from '@/ui/card';
import { Chip } from '@/ui/code';
import { Skeleton } from '@/ui/skeleton';
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
import { formatDateTime, formatRelative } from '@/lib/formatDate';

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

// Encrypted-at-rest GitHub PATs for cloning private repos and registering
// webhooks. The plaintext value is sent on POST and never returned; every
// later operation identifies a token by id.
export default function TokensTab() {
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
      <Callout variant="warn">
        <b>The GitHub tokens service is not configured</b>
        <p>
          The server answered 503 — the encryption layer for github_tokens failed to wire. The
          usual cause is <Chip>CIX_SECRET_KEY</Chip> / <Chip>CIX_SECRET_KEYFILE</Chip> not
          resolving. Check the logs and restart.
        </p>
      </Callout>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="m-0 max-w-2xl text-[13.5px] text-dim">
          Personal Access Tokens for cloning private repositories and registering webhooks. Stored
          encrypted; the plaintext is never returned after creation.
        </p>
        <CreateTokenDialog onCreated={reload} />
      </div>

      {error && (
        <Callout variant="danger">
          <b>Could not load tokens</b>
          <p>{error}</p>
        </Callout>
      )}

      {list === null ? (
        <div className="flex flex-col gap-2.5">
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
        </div>
      ) : list.length === 0 ? (
        <Empty title="No GitHub tokens yet">
          A token is required to add private repositories, and to let the server register their
          webhooks for you.
        </Empty>
      ) : (
        <Card>
          {list.map((t) => (
            <TokenRow key={t.id} token={t} onChanged={reload} />
          ))}
        </Card>
      )}
    </div>
  );
}

function TokenRow({ token, onChanged }: { token: GithubToken; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);

  async function handleDelete() {
    setBusy(true);
    try {
      await api.delete<void>(`/github-tokens/${token.id}`);
      setConfirm(false);
      toast.success('Token deleted', { description: token.name });
      onChanged();
    } catch (e) {
      toast.error('Could not delete the token', {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="cix-row">
      <div className="min-w-0 flex-1">
        <div className="cix-row__title truncate">{token.name}</div>
        <div className="cix-row__meta truncate">
          {token.scopes.length ? token.scopes.join(', ') : 'fine-grained or no scopes'}
          {token.last_used_at ? (
            <span title={formatDateTime(token.last_used_at)}>
              {' '}
              · used {formatRelative(token.last_used_at)}
            </span>
          ) : (
            <span className="text-faint"> · never used</span>
          )}
        </div>
      </div>
      <RotateTokenDialog token={token} onRotated={onChanged} disabled={busy} />
      <Button variant="quietDanger" size="sm" disabled={busy} onClick={() => setConfirm(true)}>
        Delete
      </Button>

      <Dialog open={confirm} onOpenChange={(next) => (!busy ? setConfirm(next) : null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <span className="cix-dot is-busy" aria-hidden />
            <DialogTitle>Delete this token?</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              Projects bound to <span className="font-mono text-ink">{token.name}</span> stop
              cloning and their webhooks stop being managed until another token is attached.
            </DialogDescription>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="danger" onClick={handleDelete} disabled={busy}>
              {busy ? <Dots /> : null}
              Delete token
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// Replaces the secret value in place (PUT /github-tokens/{id}). The id and
// name are unchanged, so every project bound to this token keeps working — no
// re-binding. The new value is re-validated against GitHub and the scopes
// refresh on success.
function RotateTokenDialog({
  token,
  onRotated,
  disabled,
}: {
  token: GithubToken;
  onRotated: () => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      await api.put(`/github-tokens/${token.id}`, { token: value });
      setValue('');
      setOpen(false);
      toast.success('Token rotated', { description: token.name });
      onRotated();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setValue('');
          setErr(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" disabled={disabled}>
          Rotate
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Rotate “{token.name}”</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Replaces the secret only. The id and name stay put, so projects already using this
            token keep working. The new value is validated against GitHub and its scopes refresh
            on save.
          </DialogDescription>
          <Field label="New token value" htmlFor="rotate-value">
            <Input
              id="rotate-value"
              autoFocus
              type="password"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="ghp_… or github_pat_…"
              invalid={!!err}
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
          <Button variant="primary" onClick={submit} disabled={busy || value.trim() === ''}>
            {busy ? <Dots /> : null}
            Rotate
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CreateTokenDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Scopes are deliberately not asked for — the server validates against
  // GET /user and reads the real X-OAuth-Scopes header, which is the only
  // thing GitHub actually enforces.
  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      await api.post('/github-tokens', { name, token });
      setName('');
      setToken('');
      setOpen(false);
      toast.success('Token added', { description: name });
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
        <Button variant="primary">Add token</Button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Add a GitHub token</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            Stored encrypted at rest with AES-256-GCM. The plaintext never leaves this request and
            cannot be retrieved afterwards. Scopes are read from GitHub on save; automatic webhook
            registration needs <Chip>admin:repo_hook</Chip>.
          </DialogDescription>
          <Field label="Name" htmlFor="tok-name">
            <Input
              id="tok-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="personal"
            />
          </Field>
          <Field label="Token value" htmlFor="tok-value">
            <Input
              id="tok-value"
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="ghp_… or github_pat_…"
              invalid={!!err}
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
          <Button
            variant="primary"
            onClick={submit}
            disabled={busy || name.trim() === '' || token.trim() === ''}
          >
            {busy ? <Dots /> : null}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
