import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/api/client';
import { Badge, Status } from '@/ui/badge';
import { Card } from '@/ui/card';
import type { Workspace, WorkspaceProject, WorkspaceProjectListResponse } from '../types';
import { isInFlight } from '../types';
import { formatRelative } from '@/lib/formatDate';

// Mirrors ProjectCard so the two grids read as one system. Memberships load
// lazily — the list endpoint doesn't carry them.
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
  const total = projects?.length ?? 0;

  return (
    <Link to={`/workspaces/${workspace.id}`} className="min-w-0">
      <Card clickable className={`h-full ${summary.failed > 0 ? 'border-accent' : ''}`}>
        <div className="flex h-full flex-col gap-3 p-[18px]">
          <div className="min-w-0">
            <div className="truncate text-[15px] font-bold leading-tight">{workspace.name}</div>
            {workspace.description ? (
              <div className="cix-row__meta mt-1 truncate" title={workspace.description}>
                {workspace.description}
              </div>
            ) : null}
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            {summary.busy > 0 ? (
              <Status tone="warn" className="font-mono text-[11.5px]">
                {summary.busy} indexing
              </Status>
            ) : projects === null ? (
              <span className="cix-hint">loading…</span>
            ) : total === 0 ? (
              <Status tone="idle" className="font-mono text-[11.5px]">
                empty
              </Status>
            ) : summary.failed > 0 ? (
              <Status tone="busy" className="font-mono text-[11.5px]">
                {summary.failed} failed
              </Status>
            ) : (
              <Status tone="ok" className="font-mono text-[11.5px]">
                ready
              </Status>
            )}
            {total > 0 ? (
              <Badge variant="quiet">
                {summary.indexed}/{total} indexed
              </Badge>
            ) : null}
          </div>

          <dl className="cix-kv mt-auto">
            <dt>projects</dt>
            <dd className="tabular-nums">{projects === null ? '—' : total}</dd>
            <dt>{total > 0 ? 'updated' : 'created'}</dt>
            <dd>
              {formatRelative(total > 0 ? latestUpdate(projects!) : workspace.created_at)}
            </dd>
          </dl>
        </div>
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
  for (const p of projects) if (p.added_at > best) best = p.added_at;
  return best;
}
