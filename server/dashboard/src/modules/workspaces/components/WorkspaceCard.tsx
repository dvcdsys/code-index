import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Boxes, ChevronRight, Loader2 } from 'lucide-react';
import { api } from '@/api/client';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import type { Workspace, WorkspaceRepo, WorkspaceRepoListResponse } from '../types';
import { isInFlight } from '../types';
import { formatRelative } from '@/lib/formatDate';

// WorkspaceCard mirrors the projects ProjectCard so the dashboard reads
// with one visual language: counts at-a-glance, status badge, "click
// anywhere" surface. Repos are loaded lazily so the list page renders
// instantly and each card fills in as soon as its summary arrives.
export function WorkspaceCard({ workspace }: { workspace: Workspace }) {
  const [repos, setRepos] = useState<WorkspaceRepo[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .get<WorkspaceRepoListResponse>(`/workspaces/${workspace.id}/repos`)
      .then((r) => {
        if (!cancelled) setRepos(r.repos);
      })
      .catch(() => {
        if (!cancelled) setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [workspace.id]);

  const summary = computeSummary(repos);

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
            ) : repos === null ? (
              <Badge variant="outline" className="font-normal">
                Loading…
              </Badge>
            ) : repos.length === 0 ? (
              <Badge variant="outline" className="font-normal">
                No repos yet
              </Badge>
            ) : summary.failed > 0 ? (
              <Badge variant="destructive">{summary.failed} failed</Badge>
            ) : (
              <Badge>Ready</Badge>
            )}
            {repos !== null && repos.length > 0 && (
              <Badge variant="outline" className="font-normal text-xs">
                {summary.indexed}/{repos.length} indexed
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-4 text-xs text-muted-foreground">
            <span className="ml-auto">
              {repos !== null && repos.length > 0
                ? `Updated ${formatRelative(latestUpdate(repos))}`
                : `Created ${formatRelative(workspace.created_at)}`}
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

// computeSummary turns the repo list into the three numbers the card
// surface needs. Lives in this file because no other view computes the
// same shape.
function computeSummary(repos: WorkspaceRepo[] | null): {
  indexed: number;
  busy: number;
  failed: number;
} {
  if (!repos) return { indexed: 0, busy: 0, failed: 0 };
  let indexed = 0;
  let busy = 0;
  let failed = 0;
  for (const r of repos) {
    if (r.status === 'indexed') indexed++;
    else if (r.status === 'failed') failed++;
    else if (isInFlight(r.status)) busy++;
  }
  return { indexed, busy, failed };
}

// latestUpdate returns the most recent updated_at across a repo list.
// Used so the card's "Updated …" footer tracks the freshest signal
// rather than the workspace row's stale updated_at.
function latestUpdate(repos: WorkspaceRepo[]): string {
  let best = repos[0]?.updated_at ?? '';
  for (const r of repos) {
    if (r.updated_at > best) best = r.updated_at;
  }
  return best;
}
