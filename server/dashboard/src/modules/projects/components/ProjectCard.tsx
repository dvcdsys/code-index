import { Link } from 'react-router-dom';
import { AlertTriangle, ChevronRight, Database, FileText } from 'lucide-react';
import type { Project } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import { formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';
import { basename, isDrifted, isExternal, STATUS_VARIANT } from '../lib/projectList';
import { ReindexProjectButton } from './ReindexProjectButton';
import { SyncProjectButton } from './SyncProjectButton';

export function ProjectCard({ project }: { project: Project }) {
  const currentModel = useRuntimeModel();
  // Drift = indexed under a different model than the sidecar runs now. Shared
  // predicate so the badge and the Projects status filter never disagree.
  const drift = isDrifted(project, currentModel);
  // Format-staleness flag set server-side (e.g. chunker format changed under
  // the index). Informational — the admin triggers the full resync.
  const fullSyncRequired = !!project.full_sync_required;
  // Sync only makes sense for GitHub-cloned projects — the server can pull +
  // incrementally index those. Local projects are driven by the CLI.
  const external = isExternal(project);

  return (
    <Link to={`/projects/${project.path_hash}`} className="group">
      <Card
        className={`h-full transition-colors ${
          drift || fullSyncRequired
            ? 'border-destructive/60 hover:border-destructive'
            : 'hover:border-foreground/30'
        }`}
      >
        <CardContent className="space-y-3 p-5">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <div className="truncate text-base font-medium leading-tight">
                {basename(project.display_path ?? project.host_path)}
              </div>
              <div
                className="mt-0.5 truncate text-xs text-muted-foreground"
                title={project.display_path ?? project.host_path}
              >
                {project.display_path ?? project.host_path}
              </div>
            </div>
            <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={STATUS_VARIANT[project.status]} className="capitalize">
              {project.status}
            </Badge>
            {drift ? (
              <Badge variant="destructive" className="gap-1">
                <AlertTriangle className="h-3 w-3" />
                Stale model
              </Badge>
            ) : null}
            {fullSyncRequired ? (
              <Badge
                variant="destructive"
                className="gap-1"
                title={project.full_sync_reason ?? undefined}
              >
                <AlertTriangle className="h-3 w-3" />
                Out of sync
              </Badge>
            ) : null}
            {project.languages.slice(0, 4).map((l) => (
              <Badge key={l} variant="outline" className="font-normal text-xs">
                {l}
              </Badge>
            ))}
            {project.languages.length > 4 ? (
              <span className="text-xs text-muted-foreground">+{project.languages.length - 4}</span>
            ) : null}
          </div>
          <div className="flex items-center gap-4 text-xs text-muted-foreground">
            <span className="inline-flex items-center gap-1">
              <FileText className="h-3.5 w-3.5" />
              {project.stats.indexed_files.toLocaleString()} files
            </span>
            <span className="inline-flex items-center gap-1">
              <Database className="h-3.5 w-3.5" />
              {project.stats.total_symbols.toLocaleString()} symbols
            </span>
            <span className="ml-auto">
              {project.last_indexed_at
                ? `Indexed ${formatRelative(project.last_indexed_at)}`
                : 'Never indexed'}
            </span>
          </div>
          {external ? (
            // Dedicated action area for GitHub-synced projects. The card is a
            // <Link>, so intercept the click here to run the mutation instead
            // of navigating to the detail page.
            <div
              className="flex justify-end gap-1.5 border-t pt-3"
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
              }}
            >
              <SyncProjectButton
                hash={project.path_hash}
                hostPath={project.display_path ?? project.host_path}
                variant="outline"
                size="sm"
                className="h-7 px-2.5 text-xs"
              />
              <ReindexProjectButton
                hash={project.path_hash}
                hostPath={project.display_path ?? project.host_path}
                variant="outline"
                size="sm"
                className="h-7 px-2.5 text-xs"
              />
            </div>
          ) : null}
        </CardContent>
      </Card>
    </Link>
  );
}
