import { useEffect, useState } from 'react';
import { Check, Copy, Eye, EyeOff, Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError, api } from '@/api/client';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { RadioGroup, RadioGroupItem } from '@/ui/radio-group';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import {
  useProjectGitRepo,
  useProjectWebhookInfo,
  useUpdateProjectSync,
  useUpdateProjectToken,
  type GitRepo,
  type SyncMethod,
} from '../hooks';

// Sentinel for "no token (public repo)" — Radix Select forbids an empty-string
// value, so the detached state needs its own non-empty marker.
const NO_TOKEN = '__none__';

type GithubTokenLite = { id: string; name: string; scopes: string[] };

const METHODS: ReadonlyArray<{ value: SyncMethod; label: string; hint: string }> = [
  {
    value: 'webhook',
    label: 'Webhook (push-driven)',
    hint: 'GitHub notifies the server on every push. Needs a public URL and a token with admin:repo_hook. Falls back to polling if the hook can’t be installed.',
  },
  {
    value: 'polling',
    label: 'Polling',
    hint: 'The server fetches on an interval and reindexes when the branch moves. Works without admin rights on the repo.',
  },
  {
    value: 'manual',
    label: 'Manual only',
    hint: 'No automatic sync. Use the Reindex button (or the API) to update on demand.',
  },
];

function deriveMethod(g: GitRepo): SyncMethod {
  if (g.webhook_mode === 'auto' || g.webhook_mode === 'manual') return 'webhook';
  if (g.polling_enabled) return 'polling';
  return 'manual';
}

const DEFAULT_INTERVAL_SEC = 300;

// SyncSettingsCard lets an operator reconfigure how an external project is kept
// in sync, directly from the project page. Read-only for non-admins.
export function SyncSettingsCard({ hash, isAdmin }: { hash: string; isAdmin: boolean }) {
  const gitRepo = useProjectGitRepo(hash, true);
  const update = useUpdateProjectSync();
  const updateToken = useUpdateProjectToken();
  const data = gitRepo.data;

  const [method, setMethod] = useState<SyncMethod | null>(null);
  const [intervalSec, setIntervalSec] = useState<string>('');
  const [showSecret, setShowSecret] = useState(false);
  const [tokens, setTokens] = useState<GithubTokenLite[] | null>(null);
  const [tokenSel, setTokenSel] = useState<string>(NO_TOKEN);

  useEffect(() => {
    if (data) {
      setMethod(deriveMethod(data));
      setIntervalSec(String(data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC));
      setTokenSel(data.token_id ?? NO_TOKEN);
    }
  }, [data]);

  // Token list is admin-only (GET /github-tokens returns 403 otherwise), so
  // only fetch it for admins — non-admins see the read-only summary below.
  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    api
      .get<{ tokens: GithubTokenLite[] }>('/github-tokens')
      .then((r) => {
        if (!cancelled) setTokens(r.tokens);
      })
      .catch(() => {
        if (!cancelled) setTokens([]);
      });
    return () => {
      cancelled = true;
    };
  }, [isAdmin]);

  const selectedIsWebhook = (method ?? (data ? deriveMethod(data) : 'manual')) === 'webhook';
  // Webhook URL + secret for manual GitHub setup. Only fetched for admins
  // viewing the webhook method (the secret is sensitive).
  const webhookInfo = useProjectWebhookInfo(hash, isAdmin && selectedIsWebhook);

  if (gitRepo.isLoading) {
    return <Skeleton className="h-56 w-full" />;
  }
  if (gitRepo.error || !data) {
    // 404 for local projects — the caller already gates on external, so any
    // error here is unexpected; render nothing rather than a scary alert.
    return null;
  }

  const selected = method ?? deriveMethod(data);

  function copy(label: string, value: string) {
    void navigator.clipboard?.writeText(value).then(
      () => toast.success(`${label} copied`),
      () => toast.error(`Couldn't copy ${label}`),
    );
  }

  async function save() {
    const secs = Number(intervalSec);
    try {
      const res = await update.mutateAsync({
        hash,
        sync_method: selected,
        poll_interval_seconds:
          selected === 'polling' && Number.isFinite(secs) && secs > 0 ? secs : undefined,
      });
      if (res.note) {
        toast.info('Sync updated', { description: res.note });
      } else {
        toast.success('Sync settings saved');
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to update sync settings', { description: detail });
    }
  }

  async function saveToken() {
    try {
      await updateToken.mutateAsync({
        hash,
        token_id: tokenSel === NO_TOKEN ? null : tokenSel,
      });
      toast.success('Token updated');
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to update token', { description: detail });
    }
  }

  const currentToken = data.token_id ?? NO_TOKEN;
  const tokenName = data.token_id ? tokens?.find((t) => t.id === data.token_id)?.name : null;
  const tokenLabel = data.token_id ? (tokenName ?? 'configured') : 'none (public)';

  return (
    <Card>
      <CardHeader>
        <CardTitle>Sync settings</CardTitle>
        <CardDescription>
          Choose how this GitHub project is reindexed when it changes upstream.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <RadioGroup
          value={selected}
          onValueChange={(v) => setMethod(v as SyncMethod)}
          className="space-y-3"
          disabled={!isAdmin || update.isPending}
        >
          {METHODS.map((m) => (
            <div key={m.value} className="flex items-start gap-3">
              <RadioGroupItem id={`sync-${m.value}`} value={m.value} className="mt-0.5" />
              <div className="space-y-0.5">
                <Label htmlFor={`sync-${m.value}`} className="font-medium">
                  {m.label}
                </Label>
                <p className="text-xs text-muted-foreground">{m.hint}</p>
              </div>
            </div>
          ))}
        </RadioGroup>

        {isAdmin && (
          <div className="space-y-1.5">
            <Label htmlFor="repo-token">GitHub token</Label>
            <div className="flex items-center gap-2">
              <Select
                value={tokenSel}
                onValueChange={setTokenSel}
                disabled={updateToken.isPending}
              >
                <SelectTrigger id="repo-token" className="flex-1">
                  <SelectValue placeholder="Choose a token…" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_TOKEN}>(public repo · no token)</SelectItem>
                  {tokens?.map((t) => (
                    <SelectItem key={t.id} value={t.id}>
                      {t.name}
                      {t.scopes.length > 0 && (
                        <span className="ml-2 text-xs text-muted-foreground">
                          {t.scopes.join(', ')}
                        </span>
                      )}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void saveToken()}
                disabled={updateToken.isPending || tokenSel === currentToken}
              >
                {updateToken.isPending ? (
                  <Loader2 className="mr-1 h-4 w-4 animate-spin" />
                ) : null}
                Update token
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Which stored PAT clones/fetches this repo and manages its webhook.
              The id stays bound to the project — rotating the token under{' '}
              <strong>GitHub Integration → Tokens</strong> keeps this working
              without re-selecting it here.
            </p>
            {tokens?.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No tokens stored yet. Add one under{' '}
                <strong>GitHub Integration → Tokens</strong>.
              </p>
            )}
          </div>
        )}

        {selected === 'webhook' && isAdmin && (
          <div className="space-y-3 rounded-md border bg-muted/30 p-3">
            <p className="text-xs text-muted-foreground">
              Auto-registration needs a public URL (CIX_PUBLIC_URL or a live
              tunnel) and a token with <code>admin:repo_hook</code>. Otherwise
              add the hook by hand in GitHub → Settings → Webhooks (content
              type <code>application/json</code>, event <code>push</code>).
            </p>
            {webhookInfo.isLoading ? (
              <Skeleton className="h-16 w-full" />
            ) : webhookInfo.data ? (
              <div className="space-y-2 text-sm">
                <div className="space-y-1">
                  <Label className="text-xs">Payload URL</Label>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 truncate rounded bg-background px-2 py-1 text-xs">
                      {webhookInfo.data.webhook_url}
                    </code>
                    <Button
                      variant="outline"
                      size="icon"
                      className="h-7 w-7 shrink-0"
                      onClick={() => copy('Payload URL', webhookInfo.data!.webhook_url)}
                      title="Copy URL"
                    >
                      <Copy className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                  {!webhookInfo.data.webhook_url.startsWith('http') && (
                    <p className="text-xs text-destructive">
                      No public origin configured — set CIX_PUBLIC_URL or a tunnel,
                      then prepend it to this path.
                    </p>
                  )}
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Secret</Label>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 truncate rounded bg-background px-2 py-1 font-mono text-xs">
                      {showSecret ? webhookInfo.data.webhook_secret : '•'.repeat(24)}
                    </code>
                    <Button
                      variant="outline"
                      size="icon"
                      className="h-7 w-7 shrink-0"
                      onClick={() => setShowSecret((v) => !v)}
                      title={showSecret ? 'Hide secret' : 'Reveal secret'}
                    >
                      {showSecret ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      className="h-7 w-7 shrink-0"
                      onClick={() => copy('Secret', webhookInfo.data!.webhook_secret)}
                      title="Copy secret"
                    >
                      <Copy className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  {webhookInfo.data.auto_registered ? (
                    <>
                      <Check className="h-3.5 w-3.5 text-green-600" />
                      Hook auto-registered by the server.
                    </>
                  ) : (
                    'Hook not auto-registered — add it manually with the values above.'
                  )}
                </div>
              </div>
            ) : null}
          </div>
        )}

        {selected === 'polling' && (
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1">
              <Label htmlFor="poll-interval" className="text-xs">
                Poll interval (seconds)
              </Label>
              <Input
                id="poll-interval"
                type="number"
                min={1}
                value={intervalSec}
                onChange={(e) => setIntervalSec(e.target.value)}
                disabled={!isAdmin || update.isPending}
                className="w-40"
              />
            </div>
            <p className="text-xs text-muted-foreground">
              Measured from the end of the last index run. Empty / 0 uses the
              server default; values below the configured floor are clamped up.
            </p>
          </div>
        )}

        <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
          <dt className="text-muted-foreground">GitHub token</dt>
          <dd>{tokenLabel}</dd>
          <dt className="text-muted-foreground">Webhook mode</dt>
          <dd className="font-mono">{data.webhook_mode}</dd>
          <dt className="text-muted-foreground">Polling</dt>
          <dd>
            {data.polling_enabled
              ? `every ${data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC}s`
              : 'off'}
          </dd>
          {data.polling_enabled && data.next_poll_at && (
            <>
              <dt className="text-muted-foreground">Next poll</dt>
              <dd title={formatDateTime(data.next_poll_at)}>{formatRelative(data.next_poll_at)}</dd>
            </>
          )}
          {data.last_error && (
            <>
              <dt className="text-destructive">Last error</dt>
              <dd className="break-all text-destructive">{data.last_error}</dd>
            </>
          )}
        </dl>

        {isAdmin ? (
          <div className="flex items-center gap-3">
            <Button size="sm" onClick={() => void save()} disabled={update.isPending}>
              {update.isPending ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="mr-1 h-4 w-4" />
              )}
              Save sync settings
            </Button>
            {selected !== deriveMethod(data) && (
              <span className="text-xs text-muted-foreground">Unsaved change</span>
            )}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">Changing sync settings requires an admin.</p>
        )}
      </CardContent>
    </Card>
  );
}
