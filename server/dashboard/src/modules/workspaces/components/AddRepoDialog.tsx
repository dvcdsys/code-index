import { useEffect, useMemo, useState } from 'react';
import { api, ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
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
import { Chip } from '@/ui/code';
import { Field, Input } from '@/ui/input';
import { RadioCard, RadioGroup } from '@/ui/radio-group';
import { useCopy } from '@/lib/useCopy';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import type {
  GithubAccount,
  GithubAccountListResponse,
  GithubRepo,
  GithubRepoListResponse,
  GithubToken,
  GithubTokenListResponse,
  GitRepoCreated,
  WebhookMode,
} from '../types';

// Sentinel value for the "(public repo, no token)" Select option. Radix
// Select forbids an empty-string item value, so we encode the no-token
// choice as a distinct string and translate at the request boundary.
const NO_TOKEN = '__none__';

// AddRepoDialog is a staged form: each step gates the next so the user
// can't pick a repository before choosing a token, and can't submit
// before pinning down a branch + webhook mode. The shape mirrors how
// people actually fill it in: PAT → account → repo → branch → webhook.
//
// Scope:
//   - workspaceID provided  → after POST /git-repos, additionally
//     POST /workspaces/{id}/projects so the new project is linked
//     into that workspace. Note: the link call only succeeds once
//     the project finishes indexing; we kick off the link request
//     fire-and-forget here, the dashboard's polling will surface
//     the membership once indexing completes.
//   - workspaceID omitted  → just POST /git-repos. The new project
//     lives standalone in /projects and can be linked later.
export function AddRepoDialog({
  workspaceID,
  onAdded,
}: {
  workspaceID?: string;
  onAdded: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [tokens, setTokens] = useState<GithubToken[] | null>(null);
  const [tokenID, setTokenID] = useState<string>('');

  // Account step — loaded after a token is picked. The list contains
  // the PAT owner (user) plus every org from /user/orgs; the dashboard
  // requires the operator to pick one specifically so we always know
  // which slice of GitHub to query for the repo picker. Default is
  // the first account returned (the user themselves).
  const [accounts, setAccounts] = useState<GithubAccount[] | null>(null);
  const [accountsErr, setAccountsErr] = useState<string | null>(null);
  const [accountsLoading, setAccountsLoading] = useState(false);
  const [accountKey, setAccountKey] = useState<string>('');

  // The repo step. `repos` is the unfiltered fetch result; the visible
  // dropdown is filtered client-side by `repoQuery` so typing is
  // instant and we only hit GitHub once per account selection.
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
  const [created, setCreated] = useState<GitRepoCreated | null>(null);

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

  // When a token is picked, fetch the accounts visible to it. The
  // first account in the response (always the PAT owner) is auto-
  // selected so the repo picker can populate immediately without an
  // extra user click.
  useEffect(() => {
    if (!tokenID || tokenID === NO_TOKEN) {
      setAccounts(null);
      setAccountKey('');
      setRepos(null);
      setSelectedRepo(null);
      setAccountsLoading(false);
      return;
    }
    let cancelled = false;
    setAccountsErr(null);
    setAccountKey('');
    setRepos(null);
    setSelectedRepo(null);
    setAccountsLoading(true);
    api
      .get<GithubAccountListResponse>(`/github-tokens/${tokenID}/accounts`)
      .then((r) => {
        if (cancelled) return;
        setAccounts(r.accounts);
        setAccountsLoading(false);
        if (r.accounts.length > 0) {
          setAccountKey(`${r.accounts[0].type}:${r.accounts[0].login}`);
        }
      })
      .catch((e) => {
        if (cancelled) return;
        const msg =
          e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e);
        setAccountsErr(msg);
        setAccountsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tokenID]);

  // When the account selection changes, load that account's repos.
  // /users/{login}/repos for user, /orgs/{login}/repos for org — we
  // always scope the listing so the operator gets a predictable,
  // bounded result instead of the aggregated view.
  useEffect(() => {
    if (!tokenID || tokenID === NO_TOKEN || !accountKey || !accounts) return;
    const acc = accounts.find((a) => `${a.type}:${a.login}` === accountKey);
    if (!acc) return;

    let cancelled = false;
    setReposLoading(true);
    setReposErr(null);
    setSelectedRepo(null);

    api
      .get<GithubRepoListResponse>(`/github-tokens/${tokenID}/repos`, {
        query: { account: acc.login, account_type: acc.type },
      })
      .then((r) => {
        if (!cancelled) {
          setRepos(r.repos);
          setReposLoading(false);
        }
      })
      .catch((e) => {
        if (cancelled) return;
        const msg =
          e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e);
        setReposErr(msg);
        setReposLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tokenID, accountKey, accounts]);

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
      const resp = await api.post<GitRepoCreated>(`/git-repos`, payload);
      setCreated(resp);
      // If we're in workspace context, link the freshly-created
      // project. The link call may 422 if indexing isn't done yet —
      // that's fine for the dashboard UX. We fire it off and rely on
      // polling in the parent page to pick up the membership once
      // indexing finishes. A more robust approach (retry-on-422) lives
      // in a follow-up if users complain.
      if (workspaceID) {
        try {
          await api.post(`/workspaces/${workspaceID}/projects`, {
            project_hash: resp.git_repo.path_hash,
          });
        } catch {
          // Swallow — workspace polling will pick up the project once
          // indexing completes and operators can manually link via
          // "Add Existing Project" if anything goes wrong.
        }
      }
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
    setAccounts(null);
    setAccountsErr(null);
    setAccountsLoading(false);
    setAccountKey('');
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
          <Button variant="primary">Add repo</Button>
        </DialogTrigger>
        <DialogContent className="max-w-lg [&>*]:min-w-0">
          <DialogHeader>
            <DialogTitle>Repository attached</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              Clone and indexing are queued. The project moves through cloning → indexing →
              indexed as the worker picks it up.
            </DialogDescription>
            <CreatedResult created={created} mode={webhookMode} />
          </DialogBody>
          <DialogFooter>
            <Button
              variant="primary"
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
        <Button variant="primary">Add repo</Button>
      </DialogTrigger>
      {/* `min-w-0` on every direct grid child is the trick: DialogContent
          is `display: grid`, and grid items default to `min-width: auto`
          (= min-content). A long unbreakable repo full_name then blows
          out the grid track and the whole dialog widens past max-w-lg.
          Applying min-w-0 lets the track shrink and the inner truncate
          actually take effect. */}
      <DialogContent className="max-w-lg [&>*]:min-w-0">
        <DialogHeader>
          <DialogTitle>Add repository</DialogTitle>
        </DialogHeader>

        <DialogBody className="min-w-0">
          <DialogDescription>
            Pick a token, then an account and a repository. The branch defaults to{' '}
            <Chip>main</Chip> — each row shows its repo&rsquo;s own default on the right.
          </DialogDescription>
          {/* Step 1: token */}
          <Field
            label="GitHub token"
            htmlFor="tok"
            hint={
              tokens?.length === 0
                ? 'No tokens stored yet — add one under GitHub Integration.'
                : undefined
            }
          >
            <Select value={tokenID} onValueChange={setTokenID}>
              <SelectTrigger id="tok">
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
          </Field>

          {/* Accounts fetch is paginated server-side (/user + up to 5
              pages of /user/repos) and can take a few seconds against
              a SSO-protected org. Surface a spinner so the form
              doesn't look frozen between picking the token and the
              account selector appearing. */}
          {tokenID && tokenID !== NO_TOKEN && accountsLoading && (
            <span className="flex items-center gap-2 font-mono text-[12px] text-muted">
              <Dots /> loading accounts visible to this token…
            </span>
          )}

          {/* Step 2: account — the PAT owner plus every org they
              belong to. The operator must pick one specifically so we
              always know which slice of GitHub to ask. */}
          {tokenID && tokenID !== NO_TOKEN && accounts !== null && (
            <Field
              label="Account"
              htmlFor="acc"
              error={accountsErr ?? undefined}
              hint={
                accounts.length === 0
                  ? 'GitHub returned no accounts — the PAT needs at least read:user and read:org.'
                  : undefined
              }
            >
              <Select value={accountKey} onValueChange={setAccountKey}>
                <SelectTrigger id="acc">
                  <SelectValue placeholder="Choose an account…" />
                </SelectTrigger>
                <SelectContent>
                  {accounts.map((a) => (
                    <SelectItem key={`${a.type}:${a.login}`} value={`${a.type}:${a.login}`}>
                      {a.login} · {a.type}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}

          {/* Step 3: repository — only shown once accounts are loaded
              (and therefore an account auto-selected). Showing the
              Repository label before that just renders an empty box
              that adds to the "form is frozen" feeling. */}
          {tokenID && tokenID !== NO_TOKEN && accounts !== null && (
            <Field label="Repository" htmlFor="repo-search">
              {reposLoading ? (
                <span className="flex items-center gap-2 font-mono text-[12px] text-muted">
                  <Dots /> loading repositories…
                </span>
              ) : reposErr ? (
                <Callout variant="danger">
                  <p>{reposErr}</p>
                </Callout>
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
                  <div className="mt-2 max-h-56 min-w-0 overflow-y-auto overflow-x-hidden border">
                    {filteredRepos.length === 0 ? (
                      <div className="px-3 py-2 font-mono text-[11.5px] text-muted">
                        no match · {repos.length} visible to this token
                      </div>
                    ) : (
                      <ul>
                        {filteredRepos.map((r) => {
                          const active = selectedRepo?.full_name === r.full_name;
                          return (
                            <li key={r.full_name}>
                              <button
                                type="button"
                                title={r.full_name}
                                onClick={() => {
                                  setSelectedRepo(r);
                                  // Branch defaults to "main" and stays
                                  // there when the user picks a repo —
                                  // we deliberately do NOT auto-fill
                                  // from the repo's default_branch so
                                  // the form has a single, predictable
                                  // default. The user can edit the
                                  // branch input if the repo needs a
                                  // different one (e.g. legacy master).
                                }}
                                className={`flex w-full min-w-0 items-center gap-2.5 px-3 py-2 text-left text-sm ${
                                  active ? 'bg-ink text-surface' : 'hover:bg-surface-hover'
                                }`}
                              >
                                <span
                                  aria-hidden
                                  className={`h-2 w-2 flex-none ${
                                    r.private ? 'bg-accent' : 'bg-ok'
                                  }`}
                                  title={r.private ? 'private' : 'public'}
                                />
                                <span className="min-w-0 flex-1 truncate font-mono text-[13px]">
                                  {r.full_name}
                                </span>
                                <span className="shrink-0 font-mono text-[11px] opacity-70">
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
            </Field>
          )}

          {/* Step 2 (no-token variant): manual URL input */}
          {tokenID === NO_TOKEN && (
            <Field
              label="GitHub URL"
              htmlFor="manual-url"
              hint="Only public repositories can be cloned without a token."
            >
              <Input
                id="manual-url"
                placeholder="https://github.com/owner/repo"
                value={manualUrl}
                onChange={(e) => setManualUrl(e.target.value)}
              />
            </Field>
          )}

          {/* Step 3: branch — needs a URL to be meaningful */}
          {validUrl && (
            <Field label="Branch" htmlFor="branch">
              <Input id="branch" value={branch} onChange={(e) => setBranch(e.target.value)} />
            </Field>
          )}

          {/* Step 4: webhook mode — needs everything above */}
          {validUrl && (
            <Field label="Webhook">
              <RadioGroup
                value={webhookMode}
                onValueChange={(v) => setWebhookMode(v as WebhookMode)}
              >
                <RadioCard
                  id="wh-manual"
                  value="manual"
                  selected={webhookMode === 'manual'}
                  title="Manual"
                  hint="You paste the URL and secret into the repo's webhook settings."
                />
                <RadioCard
                  id="wh-auto"
                  value="auto"
                  selected={webhookMode === 'auto'}
                  disabled={tokenID === NO_TOKEN}
                  title="Automatic"
                  hint="The server registers it via the GitHub API — needs admin:repo_hook on the PAT."
                />
                <RadioCard
                  id="wh-disabled"
                  value="disabled"
                  selected={webhookMode === 'disabled'}
                  title="Disabled"
                  hint="No auto-sync — Reindex only."
                />
              </RadioGroup>
            </Field>
          )}

          {submitErr && (
            <Callout variant="danger">
              <p>{submitErr}</p>
            </Callout>
          )}
        </DialogBody>

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
          <Button variant="primary" onClick={submit} disabled={!canSubmit}>
            {submitting ? <Dots /> : null}
            Attach repository
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CreatedResult({ created, mode }: { created: GitRepoCreated; mode: WebhookMode }) {
  return (
    <div className="flex flex-col gap-3">
      <dl className="cix-kv">
        <dt>project</dt>
        <dd>{created.git_repo.project_path}</dd>
      </dl>

      {mode === 'auto' ? (
        <Callout variant={created.auto_registered ? 'ok' : 'warn'}>
          <b>
            {created.auto_registered
              ? 'Webhook registered with GitHub'
              : 'Auto-registration failed'}
          </b>
          {!created.auto_registered && created.auto_register_note ? (
            <p>{created.auto_register_note}</p>
          ) : null}
        </Callout>
      ) : null}

      {mode === 'manual' ? (
        <>
          <Callout>
            <b>Configure the webhook in GitHub</b>
            <p>
              Settings → Webhooks → Add webhook, with the URL and secret below. Content type{' '}
              <Chip>application/json</Chip>, event <Chip>push</Chip>.
            </p>
          </Callout>
          <CopyableField label="Webhook URL" value={created.webhook_url} />
          <CopyableField label="Secret" value={created.webhook_secret} />
          <p className="cix-hint m-0">
            the secret is shown once here — it can also be re-fetched from the webhook-info
            endpoint
          </p>
        </>
      ) : null}

      {mode === 'disabled' ? (
        <Callout variant="warn">
          <b>Webhook disabled</b>
          <p>This repository is only reindexed when you press Reindex.</p>
        </Callout>
      ) : null}
    </div>
  );
}

function CopyableField({ label, value }: { label: string; value: string }) {
  const { copied, copy } = useCopy();
  return (
    <Field label={label}>
      <div className="flex gap-2">
        <Input readOnly value={value} onFocus={(e) => e.currentTarget.select()} />
        <Button onClick={() => void copy(value)}>{copied ? 'Copied' : 'Copy'}</Button>
      </div>
    </Field>
  );
}
