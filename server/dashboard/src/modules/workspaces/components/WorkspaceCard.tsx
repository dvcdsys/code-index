import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Boxes, ChevronRight, Loader2 } from 'lucide-react';
import { api } from '@/api/client';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import type {
  Workspace,
  WorkspaceProject,
  WorkspaceProjectListResponse,
} from '../types';
import { isInFlight } from '../types';
import { formatRelative } from '@/lib/formatDate';

// WorkspaceCard mirrors the projects ProjectCard so the dashboard reads
// with one visual language. Project memberships load lazily.
export function WorkspaceCard({ workspace }: { workspace: Workspace }) {
  const [projects, setProjects] = useState<WorkspaceProject[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .get<WorkspaceProjectListResponse>(`/workspaces/${workspace.id}/projects`)
      .then((r) => {
        if (!cancelled) setProjects(r.projects);
      })
      .catch(() => {
        if (!cancelled) setProjects([]);
      });
    return () => {
      cancelled = true;
    };
  }, [workspace.id]);

  const summary = computeSummary(projects);

  return (
    <Link to={`/workspaces/${workspace.id}`} className="group">
      <Card className="h-full transition-colors hover:border-foreground/30">
        <CardContent className="space-y-3 p-5">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 truncate text-base font-medium leading-tight">
                <Boxes className="size-4 shrink-0 text-muted-foreground" />
                <span className="truncate">{workspace.name}</span>
              </div>
              {workspace.description && (
                <div
                  className="mt-0.5 truncate text-xs text-muted-foreground"
                  title={workspace.description}
                >
                  {workspace.description}
                </div>
              )}
            </div>
            <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            {summary.busy ? (
              <Badge variant="secondary" className="gap-1">
                <Loader2 className="size-3 animate-spin" />
                {summary.busy === 1 ? '1 in progress' : `${summary.busy} in progress`}
              </Badge>
            ) : projects === null ? (
              <Badge variant="outline" className="font-normal">
                Loading…
              </Badge>
            ) : projects.length === 0 ? (
              <Badge variant="outline" className="font-normal">
                No projects yet
              </Badge>
            ) : summary.failed > 0 ? (
              <Badge variant="destructive">{summary.failed} failed</Badge>
            ) : (
              <Badge>Ready</Badge>
            )}
            {projects !== null && projects.length > 0 && (
              <Badge variant="outline" className="font-normal text-xs">
                {summary.indexed}/{projects.length} indexed
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-4 text-xs text-muted-foreground">
            <span className="ml-auto">
              {projects !== null && projects.length > 0
                ? `Updated ${formatRelative(latestUpdate(projects))}`
                : `Created ${formatRelative(workspace.created_at)}`}
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

function computeSummary(projects: WorkspaceProject[] | null): {
  indexed: number;
  busy: number;
  failed: number;
} {
  if (!projects) return { indexed: 0, busy: 0, failed: 0 };
  let indexed = 0;
  let busy = 0;
  let failed = 0;
  for (const p of projects) {
    const s = p.project.status;
    if (s === 'indexed') indexed++;
    else if (s === 'failed' || s === 'error') failed++;
    else if (isInFlight(s)) busy++;
  }
  return { indexed, busy, failed };
}

function latestUpdate(projects: WorkspaceProject[]): string {
  let best = projects[0]?.added_at ?? '';
  for (const p of projects) {
    if (p.added_at > best) best = p.added_at;
  }
  return best;
}
