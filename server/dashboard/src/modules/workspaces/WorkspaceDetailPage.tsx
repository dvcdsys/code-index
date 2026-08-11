import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { toast } from 'sonner';
import { ApiError, api } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Empty } from '@/ui/card';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';
import { Page, SectionLabel } from '@/ui/page';
import { Skeleton } from '@/ui/skeleton';
import { AddExistingProjectDialog } from './components/AddExistingProjectDialog';
import { AddRepoDialog } from './components/AddRepoDialog';
import { WorkspaceProjectsTable } from './components/WorkspaceProjectsTable';
import { WorkspaceShareCard } from './components/WorkspaceShareCard';
import { WorkspaceSearchDialog } from './components/WorkspaceSearchDialog';
import { isInFlight } from './types';
import type { Workspace, WorkspaceProject, WorkspaceProjectListResponse } from './types';

const POLL_MS = 3000;

export function WorkspaceDetailPage() {
  const { id = '' } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user } = useAuth();
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [projects, setProjects] = useState<WorkspaceProject[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const loadProjects = useCallback(async () => {
    try {
      const r = await api.get<WorkspaceProjectListResponse>(`/workspaces/${id}/projects`);
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

  // Poll only while something is actually cloning or indexing.
  useEffect(() => {
    if (!projects || projects.length === 0) return;
    if (!projects.some((p) => isInFlight(p.project.status))) return;
    const handle = setInterval(() => void loadProjects(), POLL_MS);
    return () => clearInterval(handle);
  }, [projects, loadProjects]);

  // "Indexing finished" used to be a bespoke inline banner with its own
  // timeout. It is a transient notification, so it is a toast now — one less
  // thing shifting the layout.
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
      toast.success('Indexing finished', { description: 'Workspace search is ready.' });
    }
  }, [projects]);

  const indexed = projects?.filter((p) => p.project.status === 'indexed').length ?? 0;
  const total = projects?.length ?? 0;
  useStatusFact(projects ? `${indexed}/${total} indexed` : null);

  async function handleDelete() {
    if (!workspace) return;
    setDeleting(true);
    try {
      await api.delete<void>(`/workspaces/${workspace.id}`);
      toast.success('Workspace deleted', { description: workspace.name });
      navigate('/workspaces');
    } catch (e) {
      toast.error('Could not delete the workspace', {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setDeleting(false);
      setConfirmDelete(false);
    }
  }

  if (notFound) {
    return (
      <Page title="Workspace not found">
        <Callout variant="danger">
          <b>No such workspace</b>
          <p>It may have been deleted. Go back to the list and pick another.</p>
          <Link to="/workspaces" className="cix-link-btn mt-1 self-start">
            All workspaces
          </Link>
        </Callout>
      </Page>
    );
  }

  if (workspace === null) {
    return (
      <Page title="Workspace">
        <Skeleton className="h-12 w-1/2" />
        <Skeleton className="mt-4 h-32" />
      </Page>
    );
  }

  const canShare = user && (user.role === 'admin' || workspace.owner_user_id === user.id);

  return (
    <Page
      title={workspace.name}
      subtitle={workspace.description || 'One query across every project linked here.'}
      action={<WorkspaceSearchDialog workspace={workspace} />}
    >
      <div className="mb-5 flex flex-wrap items-center gap-2.5">
        <Button asChild variant="ghost" size="sm" className="-ml-3.5">
          <Link to="/workspaces">← All workspaces</Link>
        </Button>
        <span className="flex-1" />
        {/* Add repo clones + indexes a new external project AND links it here
            in one step. POST /git-repos is admin-only, so non-admins only get
            the "link an existing project" path below. */}
        {user?.role === 'admin' ? (
          <AddRepoDialog workspaceID={workspace.id} onAdded={loadProjects} />
        ) : null}
        <AddExistingProjectDialog
          workspaceID={workspace.id}
          existingProjectPaths={(projects ?? []).map((p) => p.project.host_path)}
          onAdded={loadProjects}
        />
        <Button variant="quietDanger" onClick={() => setConfirmDelete(true)}>
          Delete
        </Button>
      </div>

      {error ? (
        <Callout variant="danger" className="mb-5">
          <b>Could not load the projects</b>
          <p>{error}</p>
        </Callout>
      ) : null}

      <SectionLabel
        aside={
          projects ? (
            <span className="font-mono text-[11.5px] text-muted">
              {total === 0 ? 'none' : `${indexed} of ${total} indexed`}
            </span>
          ) : null
        }
      >
        Projects
      </SectionLabel>

      {projects === null ? (
        <div className="flex flex-col gap-2.5">
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
        </div>
      ) : projects.length === 0 ? (
        <Empty title="No projects in this workspace">
          Use <b>Add repo</b> to clone and index a new GitHub project, or <b>Link project</b> to
          attach one that already exists.
        </Empty>
      ) : (
        <WorkspaceProjectsTable
          workspaceID={workspace.id}
          projects={projects}
          onUnlinked={loadProjects}
        />
      )}

      {canShare ? (
        <div className="mt-5">
          <WorkspaceShareCard workspaceId={workspace.id} />
        </div>
      ) : null}

      <Dialog
        open={confirmDelete}
        onOpenChange={(next) => (!next && !deleting ? setConfirmDelete(false) : null)}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <span className="cix-dot is-busy" aria-hidden />
            <DialogTitle>Delete this workspace?</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              <span className="font-mono text-ink">{workspace.name}</span> and its links are
              removed. The projects themselves stay indexed and reachable from Projects.
            </DialogDescription>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)} disabled={deleting}>
              Cancel
            </Button>
            <Button variant="danger" onClick={handleDelete} disabled={deleting}>
              {deleting ? <Dots /> : null}
              Delete workspace
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  );
}
