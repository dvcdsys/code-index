import { useEffect, useState } from 'react';
import { AlertCircle, Boxes } from 'lucide-react';
import { ApiError, api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Skeleton } from '@/ui/skeleton';
import { WorkspaceCard } from './components/WorkspaceCard';
import { CreateWorkspaceDialog } from './components/CreateWorkspaceDialog';
import type { Workspace, WorkspaceListResponse } from './types';

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
      <div className="space-y-6">
        <Header />
        <Alert>
          <AlertCircle className="size-4" />
          <AlertTitle>Workspaces feature is disabled</AlertTitle>
          <AlertDescription>
            Set <code>CIX_WORKSPACES_ENABLED=true</code> on the server and restart
            to enable workspaces.
          </AlertDescription>
        </Alert>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <Header onCreated={reload} />

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Failed to load workspaces</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {list === null ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-44 w-full" />
          ))}
        </div>
      ) : list.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {list.map((ws) => (
            <WorkspaceCard key={ws.id} workspace={ws} />
          ))}
        </div>
      )}
    </div>
  );
}

function Header({ onCreated }: { onCreated?: () => void }) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Workspaces</h1>
        <p className="text-sm text-muted-foreground">
          Group GitHub repositories for cross-project semantic search.
        </p>
      </div>
      {onCreated && <CreateWorkspaceDialog onCreated={onCreated} />}
    </header>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <Boxes className="h-10 w-10 text-muted-foreground" />
      <div className="space-y-1">
        <p className="text-base font-medium">No workspaces yet</p>
        <p className="max-w-sm text-sm text-muted-foreground">
          Click <strong>New workspace</strong> to create one. Inside the
          workspace you'll be able to attach GitHub repositories and use
          cross-project semantic search.
        </p>
      </div>
    </div>
  );
}
