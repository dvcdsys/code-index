import { useEffect, useRef } from 'react';
import { Link, useParams } from 'react-router-dom';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { Project } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Badge, Status } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, StatStrip } from '@/ui/card';
import { Chip, Well } from '@/ui/code';
import { Page, SectionLabel } from '@/ui/page';
import { Skeleton } from '@/ui/skeleton';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';
import { DeleteProjectDialog } from './components/DeleteProjectDialog';
import { ForceStopButton } from './components/ForceStopButton';
import { IndexingProgressCard } from './components/IndexingProgressCard';
import { ProjectInfoCard } from './components/ProjectInfoCard';
import { ProjectShareCard } from './components/ProjectShareCard';
import { ReassignOwnerDialog } from './components/ReassignOwnerDialog';
import { ReindexProjectButton } from './components/ReindexProjectButton';
import { SyncProjectButton } from './components/SyncProjectButton';
import { SyncSettingsCard } from './components/SyncSettingsCard';
import { isExternal as isExternalProject, STATUS_TONE, statusLabel } from './lib/projectList';
import { useProject, useProjectGitRepo, useProjectSummary, useProjectWorkspaces } from './hooks';

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const project = useProject(id);
  const summary = useProjectSummary(id);
  const workspaces = useProjectWorkspaces(id);
  const currentModel = useRuntimeModel();

  // Surface async job failures: a failed clone/index leaves the project at
  // status:"error" and writes git_repos.last_error, but POST /reindex already
  // returned 200 ("enqueued") so the operator otherwise sees nothing. Watch
  // the git-repo row while a job is in flight and toast on the transition.
  const isExternal = !!project.data && isExternalProject(project.data);
  const jobInFlight = project.data?.status === 'indexing' || project.data?.status === 'error';
  const gitRepo = useProjectGitRepo(id, isExternal, isExternal && jobInFlight);

  const prevStatus = useRef<Project['status'] | undefined>(undefined);
  useEffect(() => {
    const status = project.data?.status;
    // prevStatus starts undefined, so landing on an already-errored project
    // shows the callout without a spurious toast — only a live not-error →
    // error transition (a job we were watching just failed) toasts.
    if (prevStatus.current && prevStatus.current !== 'error' && status === 'error') {
      toast.error('Index run failed', {
        description:
          gitRepo.data?.last_error ?? 'The background clone/index job failed — details on the page.',
      });
    }
    prevStatus.current = status;
  }, [project.data?.status, gitRepo.data?.last_error]);

  const p = project.data;
  const displayPath = p ? (p.display_path ?? p.host_path) : '';
  useStatusFact(p ? displayPath : null);

  if (project.isLoading) return <DetailSkeleton />;

  if (project.error || !p) {
    return (
      <Page title="Project not found">
        <Callout variant="danger">
          <b>Could not load this project</b>
          <p>{project.error instanceof ApiError ? project.error.detail : 'Unknown error.'}</p>
          <Link to="/projects" className="cix-link-btn mt-1 self-start">
            Back to projects
          </Link>
        </Callout>
      </Page>
    );
  }

  const s = summary.data;
  const drift = !!p.indexed_with_model && !!currentModel && p.indexed_with_model !== currentModel;
  const fullSyncRequired = !!p.full_sync_required;

  return (
    <Page
      title={
        <span className="block break-all font-mono text-[26px] leading-tight">{displayPath}</span>
      }
      subtitle={
        <span className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[12px] text-muted">
          <Status tone={STATUS_TONE[p.status]}>{statusLabel(p.status).toLowerCase()}</Status>
          <span>{p.path_hash}</span>
          {p.machine_label ? <span>on {p.machine_label}</span> : null}
          <span title={formatDateTime(p.created_at)}>added {formatRelative(p.created_at)}</span>
          <span title={p.last_indexed_at ? formatDateTime(p.last_indexed_at) : undefined}>
            {p.last_indexed_at ? `indexed ${formatRelative(p.last_indexed_at)}` : 'never indexed'}
          </span>
        </span>
      }
      action={
        <Button variant="primary" asChild>
          <Link to={`/search?project=${p.path_hash}`}>Search this project</Link>
        </Button>
      }
    >
      <div className="mb-5 flex flex-wrap items-center gap-2.5">
        <Button asChild variant="ghost" size="sm" className="-ml-3.5">
          <Link to="/projects">← All projects</Link>
        </Button>
        <span className="flex-1" />
        {isExternal ? (
          <>
            <SyncProjectButton hash={p.path_hash} hostPath={displayPath} size="default" />
            <ReindexProjectButton hash={p.path_hash} hostPath={displayPath} size="default" />
            {p.status === 'indexing' ? (
              <ForceStopButton hash={p.path_hash} hostPath={displayPath} />
            ) : null}
          </>
        ) : null}
        {isAdmin && !isExternal ? (
          <ReassignOwnerDialog hash={p.path_hash} currentOwnerId={p.owner_user_id} />
        ) : null}
        {isAdmin ? (
          <DeleteProjectDialog hash={p.path_hash} hostPath={displayPath} redirectAfter />
        ) : null}
      </div>

      <div className="flex flex-col gap-5">
        {p.status === 'error' ? (
          <Callout variant="danger">
            <b>The last index run failed</b>
            {gitRepo.data?.last_error ? (
              <Well className="max-h-40 overflow-auto whitespace-pre-wrap break-all">
                {gitRepo.data.last_error}
              </Well>
            ) : (
              <p>
                The last clone/index job failed.{' '}
                {isExternal
                  ? 'Check the GitHub token and repository access, then retry with Sync or Reindex.'
                  : 'Check the server logs, then re-run the indexer.'}
              </p>
            )}
            {!isExternal ? (
              <p>
                Re-run from a terminal: <Chip>cix reindex {displayPath}</Chip>
              </p>
            ) : null}
          </Callout>
        ) : null}

        {p.status === 'indexing' ? (
          isExternal ? (
            <IndexingProgressCard hash={p.path_hash} />
          ) : (
            <Callout variant="warn">
              <b>Indexing in progress</b>
              <p>
                Stats and search results stay incomplete until the run finishes. This page
                refreshes itself when it does.
              </p>
            </Callout>
          )
        ) : null}

        {drift ? (
          <Callout variant="warn">
            <b>Indexed with a stale embedding model</b>
            <p>
              These vectors were produced under <Chip>{p.indexed_with_model}</Chip> while the
              sidecar now runs <Chip>{currentModel}</Chip>. Semantic results are unreliable until
              the project is reindexed
              {!isExternal ? (
                <>
                  {' '}
                  — <Chip>cix reindex {displayPath}</Chip>
                </>
              ) : (
                ' with the Reindex button above'
              )}
              .
            </p>
          </Callout>
        ) : null}

        {fullSyncRequired ? (
          <Callout variant="danger">
            <b>Out of sync — a full resync is required</b>
            <p>
              {p.full_sync_reason ??
                'This index is format-stale and has to be rebuilt from scratch.'}{' '}
              An incremental sync will not clear the flag; it clears itself once a full reindex
              completes
              {!isExternal ? (
                <>
                  {' '}
                  (<Chip>cix reindex {displayPath}</Chip>)
                </>
              ) : (
                ' (use Reindex above)'
              )}
              .
            </p>
          </Callout>
        ) : null}

        <StatStrip
          items={[
            {
              label: 'Files indexed',
              value: (
                <>
                  {p.stats.indexed_files.toLocaleString()}
                  <span className="ml-1.5 text-[12px] font-normal text-muted">
                    / {p.stats.total_files.toLocaleString()}
                  </span>
                </>
              ),
            },
            { label: 'Chunks', value: p.stats.total_chunks.toLocaleString() },
            {
              label: 'Symbols',
              value: (s?.total_symbols ?? p.stats.total_symbols).toLocaleString(),
            },
            { label: 'Languages', value: String(p.languages.length) },
          ]}
        />

        <ProjectInfoCard project={p} />

        {isExternal ? <SyncSettingsCard hash={p.path_hash} isAdmin={isAdmin} /> : null}
        {isExternal && isAdmin ? <ProjectShareCard hash={p.path_hash} /> : null}

        <div>
          <SectionLabel>Workspaces</SectionLabel>
          {workspaces.isLoading ? (
            <Skeleton className="max-w-md" />
          ) : !workspaces.data || workspaces.data.workspaces.length === 0 ? (
            <p className="m-0 text-sm text-dim">Not linked into any workspace yet.</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {workspaces.data.workspaces.map((w) => (
                <Button
                  key={w.workspace_id}
                  asChild
                  size="sm"
                  title={`Linked into ${w.workspace_name} on ${formatDateTime(w.added_at)}`}
                >
                  <Link to={`/workspaces/${w.workspace_id}`}>
                    {w.workspace_name}
                    <span className="font-mono text-[11px] text-muted">
                      {formatRelative(w.added_at)}
                    </span>
                  </Link>
                </Button>
              ))}
            </div>
          )}
        </div>

        <div className="grid gap-5 lg:grid-cols-2">
          <div>
            <SectionLabel>Top directories</SectionLabel>
            {summary.isLoading ? (
              <Skeleton className="h-48" />
            ) : !s || s.top_directories.length === 0 ? (
              <p className="m-0 text-sm text-dim">No directories yet.</p>
            ) : (
              <Card>
                {s.top_directories.map((d) => (
                  <div key={d.path} className="cix-row justify-between">
                    <code className="truncate font-mono text-[13px]" title={d.path}>
                      {d.path}
                    </code>
                    <span className="cix-row__meta flex-none tabular-nums">
                      {d.file_count.toLocaleString()} {d.file_count === 1 ? 'file' : 'files'}
                    </span>
                  </div>
                ))}
              </Card>
            )}
          </div>

          <div>
            <SectionLabel>Recent symbols</SectionLabel>
            {summary.isLoading ? (
              <Skeleton className="h-48" />
            ) : !s || s.recent_symbols.length === 0 ? (
              <p className="m-0 text-sm text-dim">No symbols indexed yet.</p>
            ) : (
              <Card>
                {s.recent_symbols.slice(0, 12).map((sym, i) => (
                  <div key={`${sym.file_path}:${sym.name}:${i}`} className="cix-row">
                    <Badge variant="quiet" className="uppercase">
                      {sym.kind}
                    </Badge>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-mono text-[13.5px] font-semibold">
                        {sym.name}
                      </div>
                      <div className="cix-row__meta truncate" title={sym.file_path}>
                        {sym.file_path}
                      </div>
                    </div>
                    <span className="cix-row__meta flex-none">{sym.language}</span>
                  </div>
                ))}
              </Card>
            )}
          </div>
        </div>

        {!isExternal ? (
          <Callout>
            <b>Reindexing a local project</b>
            <p>
              Indexing reads from the local filesystem and is driven by the CLI. Run{' '}
              <Chip>cix reindex</Chip> for a one-shot rescan, or leave <Chip>cix watch</Chip>{' '}
              running for updates on every file change.
            </p>
          </Callout>
        ) : null}
      </div>
    </Page>
  );
}

function DetailSkeleton() {
  return (
    <Page title="Project">
      <div className="flex flex-col gap-5">
        <Skeleton className="h-9 max-w-2xl" />
        <Skeleton className="h-20" />
        <div className="grid gap-5 lg:grid-cols-2">
          <Skeleton className="h-48" />
          <Skeleton className="h-48" />
        </div>
      </div>
    </Page>
  );
}
