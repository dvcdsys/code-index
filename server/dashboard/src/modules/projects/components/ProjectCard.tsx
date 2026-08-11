import { Link } from 'react-router-dom';
import type { Project } from '@/api/types';
import { Badge, Status } from '@/ui/badge';
import { Card } from '@/ui/card';
import { formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';
import {
  isDrifted,
  isExternal,
  projectLabel,
  projectPath,
  STATUS_TONE,
  statusLabel,
} from '../lib/projectList';
import { ReindexProjectButton } from './ReindexProjectButton';
import { SyncProjectButton } from './SyncProjectButton';

// Tile view. Lifts 2px into a hard shadow on hover — the one motion the
// system allows — and turns its outline accent when something is wrong.
//
// The <Link> wraps only the informational area. The action footer is a
// sibling: buttons nested inside an anchor are invalid HTML, and it also
// means the actions no longer have to fight the card's own navigation.
export function ProjectCard({ project }: { project: Project }) {
  const currentModel = useRuntimeModel();
  // Drift = indexed under a different model than the sidecar runs now. Shared
  // predicate so the badge and the status filter can never disagree.
  const drift = isDrifted(project, currentModel);
  // Format-staleness flag set server-side (the chunker format changed under
  // the index). Informational — the admin triggers the resync.
  const fullSyncRequired = !!project.full_sync_required;
  // Sync only makes sense for GitHub-cloned projects: the server can pull and
  // incrementally index those. Local ones are driven from the CLI.
  const external = isExternal(project);
  const needsAttention = drift || fullSyncRequired;

  return (
    <Card clickable className={`flex h-full flex-col ${needsAttention ? 'border-accent' : ''}`}>
      <Link
        to={`/projects/${project.path_hash}`}
        className="flex min-w-0 flex-1 flex-col gap-3 p-[18px] focus-visible:outline-offset-[-4px]"
      >
        <div className="min-w-0">
          <div className="truncate text-[15px] font-bold leading-tight">
            {projectLabel(project)}
          </div>
          <div className="cix-row__meta mt-1 truncate" title={projectPath(project)}>
            {projectPath(project)}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <Status tone={STATUS_TONE[project.status]} className="font-mono text-[11.5px]">
            {statusLabel(project.status).toLowerCase()}
          </Status>
          {drift ? <Badge variant="warn">stale model</Badge> : null}
          {fullSyncRequired ? (
            <Badge variant="busy" title={project.full_sync_reason ?? undefined}>
              out of sync
            </Badge>
          ) : null}
          {project.languages.slice(0, 3).map((l) => (
            <Badge key={l} variant="quiet">
              {l}
            </Badge>
          ))}
          {project.languages.length > 3 ? (
            <span className="cix-row__meta">+{project.languages.length - 3}</span>
          ) : null}
        </div>

        <dl className="cix-kv mt-auto">
          <dt>files</dt>
          <dd className="tabular-nums">{project.stats.indexed_files.toLocaleString()}</dd>
          <dt>symbols</dt>
          <dd className="tabular-nums">{project.stats.total_symbols.toLocaleString()}</dd>
          <dt>indexed</dt>
          <dd>
            {project.last_indexed_at ? (
              formatRelative(project.last_indexed_at)
            ) : (
              <span className="text-faint">never</span>
            )}
          </dd>
        </dl>
      </Link>

      {external ? (
        <div className="flex flex-none justify-end gap-1.5 border-t bg-surface-head px-[18px] py-2.5">
          <SyncProjectButton hash={project.path_hash} hostPath={projectPath(project)} size="sm" />
          <ReindexProjectButton
            hash={project.path_hash}
            hostPath={projectPath(project)}
            size="sm"
          />
        </div>
      ) : null}
    </Card>
  );
}
