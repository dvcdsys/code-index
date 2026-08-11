import { useEffect, useState } from 'react';
import { ApiError, api } from '@/api/client';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Empty } from '@/ui/card';
import { Page } from '@/ui/page';
import { Skeleton } from '@/ui/skeleton';
import { WorkspaceCard } from './components/WorkspaceCard';
import { CreateWorkspaceDialog } from './components/CreateWorkspaceDialog';
import type { Workspace, WorkspaceListResponse } from './types';

const SUBTITLE = 'Groups of repositories. One query searches every project in the workspace at once.';

export function WorkspacesListPage() {
  const [list, setList] = useState<Workspace[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [featureOff, setFeatureOff] = useState(false);

  async function reload() {
    try {
      const r = await api.get<WorkspaceListResponse>('/workspaces');
      setList(r.workspaces);
      setError(null);
      setFeatureOff(false);
    } catch (e) {
      // 503 means the backing service failed to wire at boot — that is an
      // operator problem, not an empty list, so it gets its own state.
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

  useStatusFact(list ? `${list.length} workspace${list.length === 1 ? '' : 's'}` : null);

  if (featureOff) {
    return (
      <Page title="Workspaces" subtitle={SUBTITLE}>
        <Callout variant="warn">
          <b>The workspaces service is not configured</b>
          <p>
            The server answered 503 — the backing service failed to wire, most often an
            encryption-key boot failure. Check the server logs and restart.
          </p>
        </Callout>
      </Page>
    );
  }

  return (
    <Page title="Workspaces" subtitle={SUBTITLE} action={<CreateWorkspaceDialog onCreated={reload} />}>
      {error ? (
        <Callout variant="danger" className="mb-5">
          <b>Could not load workspaces</b>
          <p>{error}</p>
        </Callout>
      ) : null}

      {list === null ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-40" />
          ))}
        </div>
      ) : list.length === 0 ? (
        <Empty title="No workspaces yet">
          Create one, attach a few repositories to it, then use workspace search to query all of
          them in a single request.
        </Empty>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {list.map((ws) => (
            <WorkspaceCard key={ws.id} workspace={ws} />
          ))}
        </div>
      )}
    </Page>
  );
}
