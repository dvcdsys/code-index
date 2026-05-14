import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  AlertTriangle,
  CheckCircle2,
  Folder,
  Github,
  Loader2,
  Trash2,
} from 'lucide-react';
import { api } from '@/api/client';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardContent } from '@/ui/card';
import { formatRelative } from '@/lib/formatDate';
import type { ProjectStatus, WorkspaceProject } from '../types';
import { isInFlight } from '../types';

// WorkspaceProjectRow renders one project as it appears inside a
// workspace. Unlink is the only action — the project itself is
// managed on its own detail page (reindex, webhook config, delete
// all live there). Click anywhere on the row to navigate to that
// page; the Unlink button stops propagation so it doesn't trigger
// the link.
export function WorkspaceProjectRow({
  workspaceID,
  wp,
  onUnlinked,
}: {
  workspaceID: string;
  wp: WorkspaceProject;
  onUnlinked: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const inFlight = isInFlight(wp.project.status);
  const isExternal = wp.project.host_path.startsWith('github.com/');
  const hash = wp.project.path_hash ?? '';

  async function handleUnlink(e: React.MouseEvent) {
    e.preventDefault();
    e.stopPropagation();
    if (
      !confirm(
        `Remove "${wp.project.host_path}" from this workspace?\n\nThe project itself stays — only this workspace's link is removed.`,
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      await api.delete<void>(`/workspaces/${workspaceID}/projects/${hash}`);
      onUnlinked();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      className={
        wp.project.status === 'failed' || wp.project.status === 'error'
          ? 'border-destructive/60'
          : inFlight
          ? 'border-amber-500/40'
          : ''
      }
    >
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-2">
          <RouterLink
            to={`/projects/${hash}`}
            className="min-w-0 flex-1 hover:underline"
          >
            <div className="flex items-center gap-2 truncate font-medium">
              {isExternal ? (
                <Github className="size-4 shrink-0 text-muted-foreground" />
              ) : (
                <Folder className="size-4 shrink-0 text-muted-foreground" />
              )}
              {wp.project.host_path}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              linked {formatRelative(wp.added_at)}
            </div>
          </RouterLink>
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            title="Remove from workspace (project itself stays)"
            onClick={handleUnlink}
          >
            <Trash2 className="size-4" />
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <StatusBadge status={wp.project.status} />
          {wp.project.last_indexed_at && (
            <span className="text-muted-foreground">
              · indexed {formatRelative(wp.project.last_indexed_at)}
            </span>
          )}
          {wp.project.languages.slice(0, 4).map((l) => (
            <span key={l} className="text-muted-foreground">
              {l}
            </span>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function StatusBadge({ status }: { status: ProjectStatus }) {
  if (status === 'indexed') {
    return (
      <Badge className="gap-1">
        <CheckCircle2 className="size-3" /> indexed
      </Badge>
    );
  }
  if (status === 'error' || status === 'failed') {
    return (
      <Badge variant="destructive" className="gap-1">
        <AlertTriangle className="size-3" /> {status}
      </Badge>
    );
  }
  return (
    <Badge variant="secondary" className="gap-1">
      <Loader2 className="size-3 animate-spin" />
      {status}
    </Badge>
  );
}
