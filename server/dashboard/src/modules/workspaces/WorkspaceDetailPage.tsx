import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AlertCircle, ChevronLeft, Trash2 } from 'lucide-react';
import { ApiError, api } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import { AddExistingProjectDialog } from './components/AddExistingProjectDialog';
import { AddRepoDialog } from './components/AddRepoDialog';
import { WorkspaceProjectRow } from './components/WorkspaceProjectRow';
import { WorkspaceShareCard } from './components/WorkspaceShareCard';
import { WorkspaceSearchDialog } from './components/WorkspaceSearchDialog';
import { isInFlight } from './types';
import type {
  Workspace,
  WorkspaceProject,
  WorkspaceProjectListResponse,
} from './types';

const INDEX_DONE_TOAST_MS = 5000;
const POLL_MS = 3000;

export function WorkspaceDetailPage() {
  const { id = '' } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user } = useAuth();
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [projects, setProjects] = useState<WorkspaceProject[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [indexDoneMsg, setIndexDoneMsg] = useState<string | null>(null);

  const loadProjects = useCallback(async () => {
    try {
      const r = await api.get<WorkspaceProjectListResponse>(
        `/workspaces/${id}/projects`,
      );
      setProjects(r.projects);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [id]);

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
    void loadProjects();
    return () => {
      cancelled = true;
    };
  }, [id, loadProjects]);

  // Poll while any project is still being cloned/indexed.
  useEffect(() => {
    if (!projects || projects.length === 0) return;
    const anyBusy = projects.some((p) => isInFlight(p.project.status));
    if (!anyBusy) return;
    const handle = setInterval(() => {
      void loadProjects();
    }, POLL_MS);
    return () => clearInterval(handle);
  }, [projects, loadProjects]);

  const wasInflightRef = useRef(false);
  useEffect(() => {
    if (!projects) return;
    const anyBusy = projects.some((p) => isInFlight(p.project.status));
    if (anyBusy) {
      wasInflightRef.current = true;
      return;
    }
    if (wasInflightRef.current) {
      wasInflightRef.current = false;
      setIndexDoneMsg('Indexing finished — workspace search is ready.');
    }
  }, [projects]);

  useEffect(() => {
    if (!indexDoneMsg) return;
    const handle = setTimeout(() => setIndexDoneMsg(null), INDEX_DONE_TOAST_MS);
    return () => clearTimeout(handle);
  }, [indexDoneMsg]);

  async function handleDeleteWorkspace() {
    if (!workspace) return;
    if (
      !confirm(
        `Delete workspace "${workspace.name}"?\n\nThe projects themselves stay — only this workspace is removed.`,
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
          {/* Add repo here clones + indexes a new external project AND
              links it into this workspace in one step. The dialog
              accepts an optional workspaceID — when supplied, it
              chains POST /git-repos with POST /workspaces/{id}/projects. */}
          <AddRepoDialog workspaceID={workspace.id} onAdded={loadProjects} />
          <AddExistingProjectDialog
            workspaceID={workspace.id}
            existingProjectPaths={(projects ?? []).map((p) => p.project.host_path)}
            onAdded={loadProjects}
          />
          <Button variant="outline" onClick={handleDeleteWorkspace}>
            <Trash2 className="mr-1 size-4" /> Delete
          </Button>
        </div>
      </header>

      {indexDoneMsg && (
        <Alert>
          <AlertTitle>Workspace search ready</AlertTitle>
          <AlertDescription>{indexDoneMsg}</AlertDescription>
        </Alert>
      )}

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Could not load projects</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-base font-medium">Projects</h2>
          {projects && (
            <span className="text-xs text-muted-foreground">
              {projects.length === 0
                ? 'none'
                : `${projects.filter((p) => p.project.status === 'indexed').length} of ${
                    projects.length
                  } indexed`}
            </span>
          )}
        </div>

        {projects === null ? (
          <div className="space-y-2">
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : projects.length === 0 ? (
          <ProjectsEmptyState />
        ) : (
          <div className="grid gap-3">
            {projects.map((p) => (
              <WorkspaceProjectRow
                key={p.project.host_path}
                workspaceID={workspace.id}
                wp={p}
                onUnlinked={loadProjects}
              />
            ))}
          </div>
        )}
      </section>

      {user && (user.role === 'admin' || workspace.owner_user_id === user.id) ? (
        <WorkspaceShareCard workspaceId={workspace.id} />
      ) : null}
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

function ProjectsEmptyState() {
  return (
    <div className="rounded-md border border-dashed bg-muted/20 p-8 text-center">
      <p className="text-sm font-medium">No projects in this workspace</p>
      <p className="mt-1 text-xs text-muted-foreground">
        Use <strong>Add repo</strong> to clone + index a new GitHub project, or{' '}
        <strong>Add Existing Project</strong> to link a project that already
        lives in <code>/projects</code>.
      </p>
    </div>
  );
}
