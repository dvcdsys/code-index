import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { ApiError, api } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import { Callout } from '@/ui/alert';
import { Card, CardBody, CardHead, KV } from '@/ui/card';
import { Chip } from '@/ui/code';
import { Field, Input } from '@/ui/input';
import { RadioCard, RadioGroup } from '@/ui/radio-group';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { useCopy } from '@/lib/useCopy';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import {
  useProjectGitRepo,
  useProjectWebhookInfo,
  useUpdateProjectSync,
  useUpdateProjectToken,
  type GitRepo,
  type SyncMethod,
} from '../hooks';

// Radix Select forbids an empty-string value, so "no token (public repo)"
// needs its own non-empty sentinel.
const NO_TOKEN = '__none__';

type GithubTokenLite = { id: string; name: string; scopes: string[] };

const METHODS: ReadonlyArray<{ value: SyncMethod; label: string; hint: string }> = [
  {
    value: 'webhook',
    label: 'Webhook',
    hint: 'GitHub pushes on every commit. Needs a public URL and admin:repo_hook.',
  },
  {
    value: 'polling',
    label: 'Polling',
    hint: 'The server fetches on an interval. No admin rights on the repo needed.',
  },
  {
    value: 'manual',
    label: 'Manual',
    hint: 'No automatic sync — Reindex or the API, on demand.',
  },
];

function deriveMethod(g: GitRepo): SyncMethod {
  if (g.webhook_mode === 'auto' || g.webhook_mode === 'manual') return 'webhook';
  if (g.polling_enabled) return 'polling';
  return 'manual';
}

const DEFAULT_INTERVAL_SEC = 300;

// How an external project is kept in sync with upstream. Read-only for
// non-admins — the server enforces the same thing.
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

  const url = useCopy();
  const secret = useCopy();

  useEffect(() => {
    if (data) {
      setMethod(deriveMethod(data));
      setIntervalSec(String(data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC));
      setTokenSel(data.token_id ?? NO_TOKEN);
    }
  }, [data]);

  // GET /github-tokens is admin-only (403 otherwise), so only admins fetch it;
  // non-admins get the read-only summary at the bottom.
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
  // The secret is sensitive, so it is only fetched for an admin actually
  // looking at the webhook method.
  const webhookInfo = useProjectWebhookInfo(hash, isAdmin && selectedIsWebhook);

  if (gitRepo.isLoading) return <Skeleton className="h-56" />;
  // 404 for local projects — the caller already gates on external, so any
  // error here is unexpected. Render nothing rather than a scary callout.
  if (gitRepo.error || !data) return null;

  const selected = method ?? deriveMethod(data);

  async function save() {
    const secs = Number(intervalSec);
    try {
      const res = await update.mutateAsync({
        hash,
        sync_method: selected,
        poll_interval_seconds:
          selected === 'polling' && Number.isFinite(secs) && secs > 0 ? secs : undefined,
      });
      if (res.note) toast.info('Sync updated', { description: res.note });
      else toast.success('Sync settings saved');
    } catch (err) {
      toast.error('Could not update the sync settings', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  async function saveToken() {
    try {
      await updateToken.mutateAsync({ hash, token_id: tokenSel === NO_TOKEN ? null : tokenSel });
      toast.success('Token updated');
    } catch (err) {
      toast.error('Could not update the token', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  const currentToken = data.token_id ?? NO_TOKEN;
  const tokenName = data.token_id ? tokens?.find((t) => t.id === data.token_id)?.name : null;
  const tokenLabel = data.token_id ? (tokenName ?? 'configured') : 'none (public)';
  const dirty = selected !== deriveMethod(data);

  return (
    <Card>
      <CardHead
        title="Sync settings"
        aside={
          isAdmin ? (
            <>
              {dirty ? (
                <span className="font-mono text-[11px] font-normal text-accent">unsaved</span>
              ) : null}
              <Button size="sm" onClick={() => void save()} disabled={update.isPending}>
                {update.isPending ? <Dots /> : null}
                Save
              </Button>
            </>
          ) : null
        }
      />
      <CardBody className="flex flex-col gap-5">
        <RadioGroup
          value={selected}
          onValueChange={(v) => setMethod(v as SyncMethod)}
          className="grid gap-3 lg:grid-cols-3"
          disabled={!isAdmin || update.isPending}
        >
          {METHODS.map((m) => (
            <RadioCard
              key={m.value}
              id={`sync-${m.value}`}
              value={m.value}
              selected={selected === m.value}
              disabled={!isAdmin || update.isPending}
              title={m.label}
              hint={m.hint}
            />
          ))}
        </RadioGroup>

        {isAdmin ? (
          <Field
            label="GitHub token"
            htmlFor="repo-token"
            hint={
              tokens?.length === 0
                ? 'No tokens stored yet — add one under GitHub Integration → Tokens.'
                : 'Which stored PAT clones this repo and manages its webhook. Rotating the token under GitHub Integration keeps this binding intact.'
            }
          >
            <div className="flex items-center gap-2">
              <Select value={tokenSel} onValueChange={setTokenSel} disabled={updateToken.isPending}>
                <SelectTrigger id="repo-token" className="flex-1">
                  <SelectValue placeholder="Choose a token…" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_TOKEN}>public repo · no token</SelectItem>
                  {tokens?.map((t) => (
                    <SelectItem key={t.id} value={t.id}>
                      {t.name}
                      {t.scopes.length > 0 ? ` · ${t.scopes.join(', ')}` : ''}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                onClick={() => void saveToken()}
                disabled={updateToken.isPending || tokenSel === currentToken}
              >
                {updateToken.isPending ? <Dots /> : null}
                Update
              </Button>
            </div>
          </Field>
        ) : null}

        {selected === 'webhook' && isAdmin ? (
          <div className="flex flex-col gap-3 border p-3.5">
            <p className="m-0 text-[13px] text-dim">
              Auto-registration needs a public URL (<Chip>CIX_PUBLIC_URL</Chip> or a live tunnel)
              and a token with <Chip>admin:repo_hook</Chip>. Otherwise add the hook by hand in
              GitHub → Settings → Webhooks, content type <Chip>application/json</Chip>, event{' '}
              <Chip>push</Chip>.
            </p>

            {webhookInfo.isLoading ? (
              <Skeleton className="h-16" />
            ) : webhookInfo.data ? (
              <>
                <Field
                  label="Payload URL"
                  error={
                    webhookInfo.data.webhook_url.startsWith('http')
                      ? undefined
                      : 'No public origin configured — set CIX_PUBLIC_URL or start a tunnel, then prepend it to this path.'
                  }
                >
                  <div className="flex items-center gap-2">
                    <code className="flex-1 truncate border bg-surface px-2 py-1.5 font-mono text-[12px]">
                      {webhookInfo.data.webhook_url}
                    </code>
                    <Button size="sm" onClick={() => void url.copy(webhookInfo.data!.webhook_url)}>
                      {url.copied ? 'Copied' : 'Copy'}
                    </Button>
                  </div>
                </Field>

                <Field label="Secret">
                  <div className="flex items-center gap-2">
                    <code className="flex-1 truncate border bg-surface px-2 py-1.5 font-mono text-[12px]">
                      {showSecret ? webhookInfo.data.webhook_secret : '•'.repeat(24)}
                    </code>
                    <Button size="sm" onClick={() => setShowSecret((v) => !v)}>
                      {showSecret ? 'Hide' : 'Reveal'}
                    </Button>
                    <Button
                      size="sm"
                      onClick={() => void secret.copy(webhookInfo.data!.webhook_secret)}
                    >
                      {secret.copied ? 'Copied' : 'Copy'}
                    </Button>
                  </div>
                </Field>

                <p className="cix-hint m-0">
                  {webhookInfo.data.auto_registered
                    ? 'hook auto-registered by the server'
                    : 'hook not auto-registered — add it by hand with the values above'}
                </p>
              </>
            ) : null}
          </div>
        ) : null}

        {selected === 'polling' ? (
          <Field
            label="Poll interval (seconds)"
            htmlFor="poll-interval"
            hint="Measured from the end of the last run. 0 uses the server default; anything under the configured floor is clamped up."
          >
            <Input
              id="poll-interval"
              type="number"
              min={1}
              value={intervalSec}
              onChange={(e) => setIntervalSec(e.target.value)}
              disabled={!isAdmin || update.isPending}
              className="w-40"
            />
          </Field>
        ) : null}

        <KV
          rows={[
            { label: 'github token', value: tokenLabel },
            { label: 'webhook mode', value: data.webhook_mode },
            {
              label: 'polling',
              value: data.polling_enabled
                ? `every ${data.poll_interval_seconds ?? DEFAULT_INTERVAL_SEC}s`
                : 'off',
            },
            ...(data.polling_enabled && data.next_poll_at
              ? [
                  {
                    label: 'next poll',
                    value: formatRelative(data.next_poll_at),
                    title: formatDateTime(data.next_poll_at),
                  },
                ]
              : []),
          ]}
        />

        {data.last_error ? (
          <Callout variant="danger">
            <b>Last error</b>
            <p className="break-all font-mono text-[11.5px]">{data.last_error}</p>
          </Callout>
        ) : null}

        {!isAdmin ? <p className="cix-hint m-0">changing sync settings requires an admin</p> : null}
      </CardBody>
    </Card>
  );
}
