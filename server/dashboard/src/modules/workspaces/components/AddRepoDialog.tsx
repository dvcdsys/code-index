import { useEffect, useMemo, useState } from 'react';
import { Copy, Loader2, Lock, Plus, Unlock } from 'lucide-react';
import { api, ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
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
import type {
  GithubRepo,
  GithubRepoListResponse,
  GithubToken,
  GithubTokenListResponse,
  WebhookMode,
  WorkspaceRepoCreated,
} from '../types';

// Sentinel value for the "(public repo, no token)" Select option. Radix
// Select forbids an empty-string item value, so we encode the no-token
// choice as a distinct string and translate at the request boundary.
const NO_TOKEN = '__none__';

// AddRepoDialog is a staged form: each step gates the next so the user
// can't pick a repository before choosing a token, and can't submit
// before pinning down a branch + webhook mode. The shape mirrors how
// people actually fill it in: PAT → repo → branch → webhook policy.
export function AddRepoDialog({
  workspaceID,
  onAdded,
}: {
  workspaceID: string;
  onAdded: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [tokens, setTokens] = useState<GithubToken[] | null>(null);
  const [tokenID, setTokenID] = useState<string>('');

  // The repo step. `repos` is the unfiltered fetch result; the visible
  // dropdown is filtered client-side by `repoQuery` so typing is
  // instant and we only hit GitHub once per token selection.
  const [repos, setRepos] = useState<GithubRepo[] | null>(null);
  const [reposErr, setReposErr] = useState<string | null>(null);
  const [reposLoading, setReposLoading] = useState(false);
  const [repoQuery, setRepoQuery] = useState('');
  const [selectedRepo, setSelectedRepo] = useState<GithubRepo | null>(null);
  const [manualUrl, setManualUrl] = useState(''); // used when no token

  const [branch, setBranch] = useState('main');
  const [webhookMode, setWebhookMode] = useState<WebhookMode>('manual');

  const [submitting, setSubmitting] = useState(false);
  const [submitErr, setSubmitErr] = useState<string | null>(null);
  const [created, setCreated] = useState<WorkspaceRepoCreated | null>(null);

  // Load tokens when the dialog opens — keep the request out of the
  // page mount path so users who never open the dialog don't pay for
  // the network call.
  useEffect(() => {
    if (!open) return;
    api
      .get<GithubTokenListResponse>('/github-tokens')
      .then((r) => setTokens(r.tokens))
      .catch(() => setTokens([]));
  }, [open]);

  // When a token is picked, fetch the repos it can see. Switching
  // tokens resets the picked repo + branch defaults so stale data
  // doesn't carry over.
  useEffect(() => {
    if (!tokenID || tokenID === NO_TOKEN) {
      setRepos(null);
      setSelectedRepo(null);
      return;
    }
    let cancelled = false;
    setReposLoading(true);
    setReposErr(null);
    setSelectedRepo(null);
    api
      .get<GithubRepoListResponse>(`/github-tokens/${tokenID}/repos`)
      .then((r) => {
        if (!cancelled) {
          setRepos(r.repos);
          setReposLoading(false);
        }
      })
      .catch((e) => {
        if (cancelled) return;
        const msg =
          e instanceof ApiError
            ? e.detail
            : e instanceof Error
            ? e.message
            : String(e);
        setReposErr(msg);
        setReposLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tokenID]);

  const filteredRepos = useMemo(() => {
    if (!repos) return [];
    if (!repoQuery.trim()) return repos.slice(0, 100);
    const needle = repoQuery.toLowerCase();
    return repos.filter((r) => r.full_name.toLowerCase().includes(needle)).slice(0, 100);
  }, [repos, repoQuery]);

  // The "ready to submit" gate. Either we have a picked repo OR a
  // valid manual URL, plus a non-empty branch.
  const githubUrl = selectedRepo?.html_url ?? manualUrl.trim();
  const validUrl = /^https:\/\/github\.com\/[^/]+\/[^/]+/.test(githubUrl);
  const canSubmit = validUrl && branch.trim() !== '' && !submitting;

  async function submit() {
    setSubmitting(true);
    setSubmitErr(null);
    try {
      const payload: Record<string, unknown> = {
        github_url: githubUrl,
        branch: branch.trim(),
        webhook_mode: webhookMode,
      };
      if (tokenID && tokenID !== NO_TOKEN) {
        payload.token_id = tokenID;
      }
      const resp = await api.post<WorkspaceRepoCreated>(
        `/workspaces/${workspaceID}/repos`,
        payload,
      );
      setCreated(resp);
      onAdded();
    } catch (e) {
      const msg =
        e instanceof ApiError
          ? e.detail
          : e instanceof Error
          ? e.message
          : String(e);
      setSubmitErr(msg);
    } finally {
      setSubmitting(false);
    }
  }

  function reset() {
    setTokenID('');
    setRepos(null);
    setReposErr(null);
    setSelectedRepo(null);
    setManualUrl('');
    setBranch('main');
    setWebhookMode('manual');
    setSubmitErr(null);
    setCreated(null);
    setRepoQuery('');
  }

  // The "result" view replaces the form once the repo is created so
  // the user can copy the webhook URL/secret (they're only surfaced
  // here + once via /webhook-info).
  if (created) {
    return (
      <Dialog
        open={open}
        onOpenChange={(v) => {
          setOpen(v);
          if (!v) reset();
        }}
      >
        <DialogTrigger asChild>
          <Button>
            <Plus className="mr-1 size-4" /> Add repo
          </Button>
        </DialogTrigger>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Repository attached</DialogTitle>
            <DialogDescription>
              Clone + indexing is queued. The card will progress through
              cloning → indexing → indexed as the worker picks it up.
            </DialogDescription>
          </DialogHeader>
          <CreatedResult created={created} mode={webhookMode} />
          <DialogFooter>
            <Button
              onClick={() => {
                setOpen(false);
                reset();
              }}
            >
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus className="mr-1 size-4" /> Add repo
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Add repository</DialogTitle>
          <DialogDescription>
            Pick a token, then a repository. The branch defaults to the
            repo's default branch — change it if you index a different one.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Step 1: token */}
          <div className="space-y-1.5">
            <Label htmlFor="tok">GitHub token</Label>
            <Select value={tokenID} onValueChange={setTokenID}>
              <SelectTrigger id="tok">
                <SelectValue placeholder="Choose a token…" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_TOKEN}>
                  (public repo · no token)
                </SelectItem>
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
            {tokens?.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No tokens stored yet. Add one under <strong>GitHub Tokens</strong>{' '}
                in the sidebar.
              </p>
            )}
          </div>

          {/* Step 2: repository — only shown once a token is chosen */}
          {tokenID && tokenID !== NO_TOKEN && (
            <div className="space-y-1.5">
              <Label htmlFor="repo-search">Repository</Label>
              {reposLoading ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="size-3.5 animate-spin" />
                  Loading repos accessible to this token…
                </div>
              ) : reposErr ? (
                <Alert variant="destructive">
                  <AlertDescription>{reposErr}</AlertDescription>
                </Alert>
              ) : repos === null ? null : (
                <>
                  <Input
                    id="repo-search"
                    placeholder="Filter by owner/name…"
                    value={repoQuery}
                    onChange={(e) => {
                      setRepoQuery(e.target.value);
                      setSelectedRepo(null);
                    }}
                  />
                  <div className="max-h-56 overflow-y-auto rounded-md border">
                    {filteredRepos.length === 0 ? (
                      <div className="px-3 py-2 text-xs text-muted-foreground">
                        No matching repositories. {repos.length} total visible
                        to this token.
                      </div>
                    ) : (
                      <ul>
                        {filteredRepos.map((r) => {
                          const active = selectedRepo?.full_name === r.full_name;
                          return (
                            <li key={r.full_name}>
                              <button
                                type="button"
                                onClick={() => {
                                  setSelectedRepo(r);
                                  setBranch(r.default_branch || 'main');
                                }}
                                className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-muted ${
                                  active ? 'bg-muted' : ''
                                }`}
                              >
                                {r.private ? (
                                  <Lock className="size-3.5 text-muted-foreground" />
                                ) : (
                                  <Unlock className="size-3.5 text-muted-foreground" />
                                )}
                                <span className="truncate font-medium">{r.full_name}</span>
                                <span className="ml-auto truncate font-mono text-xs text-muted-foreground">
                                  {r.default_branch}
                                </span>
                              </button>
                            </li>
                          );
                        })}
                      </ul>
                    )}
                  </div>
                </>
              )}
            </div>
          )}

          {/* Step 2 (no-token variant): manual URL input */}
          {tokenID === NO_TOKEN && (
            <div className="space-y-1.5">
              <Label htmlFor="manual-url">GitHub URL</Label>
              <Input
                id="manual-url"
                placeholder="https://github.com/owner/repo"
                value={manualUrl}
                onChange={(e) => setManualUrl(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Only public repositories can be cloned without a token.
              </p>
            </div>
          )}

          {/* Step 3: branch — needs a URL to be meaningful */}
          {validUrl && (
            <div className="space-y-1.5">
              <Label htmlFor="branch">Branch</Label>
              <Input
                id="branch"
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
              />
            </div>
          )}

          {/* Step 4: webhook mode — needs everything above */}
          {validUrl && (
            <div className="space-y-1.5">
              <Label>Webhook</Label>
              <RadioGroup
                value={webhookMode}
                onValueChange={(v) => setWebhookMode(v as WebhookMode)}
              >
                <WebhookModeOption
                  value="manual"
                  title="Manual"
                  hint="You paste the URL + secret into the repo's webhook settings."
                  selected={webhookMode === 'manual'}
                />
                <WebhookModeOption
                  value="auto"
                  title="Automatic"
                  hint="Server registers the webhook via GitHub API (requires admin:repo_hook on the PAT)."
                  selected={webhookMode === 'auto'}
                  disabled={tokenID === NO_TOKEN}
                />
                <WebhookModeOption
                  value="disabled"
                  title="Disabled"
                  hint="No auto-sync; reindex via the Reindex button only."
                  selected={webhookMode === 'disabled'}
                />
              </RadioGroup>
            </div>
          )}

          {submitErr && (
            <Alert variant="destructive">
              <AlertDescription>{submitErr}</AlertDescription>
            </Alert>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => {
              setOpen(false);
              reset();
            }}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button onClick={submit} disabled={!canSubmit}>
            {submitting ? <Loader2 className="mr-1 size-4 animate-spin" /> : null}
            Attach repository
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function WebhookModeOption({
  value,
  title,
  hint,
  selected,
  disabled,
}: {
  value: WebhookMode;
  title: string;
  hint: string;
  selected: boolean;
  disabled?: boolean;
}) {
  return (
    <label
      className={`flex cursor-pointer items-start gap-3 rounded-md border p-2 text-sm ${
        selected ? 'border-foreground/40 bg-muted/40' : 'hover:bg-muted/20'
      } ${disabled ? 'cursor-not-allowed opacity-50' : ''}`}
    >
      <RadioGroupItem value={value} disabled={disabled} className="mt-0.5" />
      <div>
        <div className="font-medium">{title}</div>
        <div className="text-xs text-muted-foreground">{hint}</div>
      </div>
    </label>
  );
}

function CreatedResult({
  created,
  mode,
}: {
  created: WorkspaceRepoCreated;
  mode: WebhookMode;
}) {
  return (
    <div className="space-y-3 text-sm">
      <div>
        <span className="text-muted-foreground">Project:</span>{' '}
        <span className="font-mono">{created.repo.project_path}</span>
      </div>

      {mode === 'auto' && (
        <Alert>
          <AlertTitle>
            {created.auto_registered
              ? 'Webhook registered with GitHub'
              : 'Auto-register failed'}
          </AlertTitle>
          {!created.auto_registered && created.auto_register_note && (
            <AlertDescription>{created.auto_register_note}</AlertDescription>
          )}
        </Alert>
      )}

      {mode === 'manual' && (
        <>
          <Alert>
            <AlertTitle>Configure the webhook in GitHub</AlertTitle>
            <AlertDescription>
              Add a webhook in <code>Settings → Webhooks → Add webhook</code>{' '}
              for the repo with the URL and secret below. Content-type:{' '}
              <code>application/json</code>. Events: <code>push</code>.
            </AlertDescription>
          </Alert>
          <CopyableField label="Webhook URL" value={created.webhook_url} />
          <CopyableField label="Secret" value={created.webhook_secret} mono />
          <p className="text-xs text-muted-foreground">
            The secret is shown once here. Store it in a password manager —
            you can also re-fetch it via the API's webhook-info endpoint.
          </p>
        </>
      )}

      {mode === 'disabled' && (
        <Alert>
          <AlertTitle>Webhook disabled</AlertTitle>
          <AlertDescription>
            This repo will only be reindexed when you click <strong>Reindex</strong>{' '}
            on its card.
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

function CopyableField({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="space-y-1">
      <Label className="text-xs">{label}</Label>
      <div className="flex gap-1">
        <Input readOnly value={value} className={mono ? 'font-mono text-xs' : ''} />
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            void navigator.clipboard.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          <Copy className="size-3.5" />
          <span className="ml-1 text-xs">{copied ? 'Copied' : 'Copy'}</span>
        </Button>
      </div>
    </div>
  );
}
