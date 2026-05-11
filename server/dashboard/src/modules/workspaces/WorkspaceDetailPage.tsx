import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AlertCircle, ChevronLeft, Trash2 } from 'lucide-react';
import { ApiError, api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import { AddRepoDialog } from './components/AddRepoDialog';
import { RepoCard } from './components/RepoCard';
import { WorkspaceSearchDialog } from './components/WorkspaceSearchDialog';
import { isInFlight } from './types';
import type {
  Workspace,
  WorkspaceRepo,
  WorkspaceRepoListResponse,
} from './types';

// Background polling cadence. Three seconds is short enough that the
// "indexing" → "indexed" transition is visible while you watch the
// dashboard, long enough that the cost of polling for a workspace
// with many repos stays modest. Only runs while at least one repo is
// in flight.
const POLL_MS = 3000;

export function WorkspaceDetailPage() {
  const { id = '' } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [repos, setRepos] = useState<WorkspaceRepo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);

  const loadRepos = useCallback(async () => {
    try {
      const r = await api.get<WorkspaceRepoListResponse>(`/workspaces/${id}/repos`);
      setRepos(r.repos);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
    }
  }, [id]);

  // Initial workspace + repo fetch.
  useEffect(() => {
    let cancelled = false;
    api
      .get<Workspace>(`/workspaces/${id}`)
      .then((ws) => {
        if (!cancelled) setWorkspace(ws);
      })
      .catch((e) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 404) {
          setNotFound(true);
          return;
        }
        setError(e instanceof Error ? e.message : String(e));
      });
    void loadRepos();
    return () => {
      cancelled = true;
    };
  }, [id, loadRepos]);

  // Live progress polling. Active only while at least one repo is
  // in pending/cloning/indexing — terminal states stop the tick so we
  // don't burn CPU on an idle workspace.
  useEffect(() => {
    if (!repos || repos.length === 0) return;
    const anyBusy = repos.some((r) => isInFlight(r.status));
    if (!anyBusy) return;
    const handle = setInterval(() => {
      void loadRepos();
    }, POLL_MS);
    return () => clearInterval(handle);
  }, [repos, loadRepos]);

  async function handleDeleteWorkspace() {
    if (!workspace) return;
    if (
      !confirm(
        `Delete workspace "${workspace.name}"?\n\nThis removes all attached repos and the indexed projects.`,
      )
    ) {
      return;
    }
    try {
      await api.delete<void>(`/workspaces/${workspace.id}`);
      navigate('/workspaces');
    } catch (e) {
      alert(e instanceof Error ? e.message : String(e));
    }
  }

  if (notFound) {
    return (
      <div className="space-y-6">
        <BackLink />
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Workspace not found</AlertTitle>
          <AlertDescription>
            It may have been deleted. Return to the list and try another.
          </AlertDescription>
        </Alert>
      </div>
    );
  }

  if (workspace === null) {
    return (
      <div className="space-y-6">
        <BackLink />
        <Skeleton className="h-12 w-1/2" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <BackLink />

      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight">{workspace.name}</h1>
          {workspace.description && (
            <p className="mt-1 text-sm text-muted-foreground">
              {workspace.description}
            </p>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <WorkspaceSearchDialog workspace={workspace} />
          <AddRepoDialog workspaceID={workspace.id} onAdded={loadRepos} />
          <Button variant="outline" onClick={handleDeleteWorkspace}>
            <Trash2 className="mr-1 size-4" /> Delete
          </Button>
        </div>
      </header>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Could not load repositories</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-base font-medium">Repositories</h2>
          {repos && (
            <span className="text-xs text-muted-foreground">
              {repos.length === 0
                ? 'none'
                : `${repos.filter((r) => r.status === 'indexed').length} of ${
                    repos.length
                  } indexed`}
            </span>
          )}
        </div>

        {repos === null ? (
          <div className="space-y-2">
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : repos.length === 0 ? (
          <ReposEmptyState />
        ) : (
          <div className="grid gap-3">
            {repos.map((repo) => (
              <RepoCard
                key={repo.id}
                repo={repo}
                onDeleted={loadRepos}
                onReindexed={loadRepos}
              />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function BackLink() {
  return (
    <Link
      to="/workspaces"
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ChevronLeft className="size-4" /> All workspaces
    </Link>
  );
}

function ReposEmptyState() {
  return (
    <div className="rounded-md border border-dashed bg-muted/20 p-8 text-center">
      <p className="text-sm font-medium">No repositories yet</p>
      <p className="mt-1 text-xs text-muted-foreground">
        Click <strong>Add repo</strong> above to attach the first one.
      </p>
    </div>
  );
}
