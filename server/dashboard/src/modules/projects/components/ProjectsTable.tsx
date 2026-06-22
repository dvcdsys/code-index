import { Link, useNavigate } from 'react-router-dom';
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ChevronsUpDown,
  Database,
  FileText,
} from 'lucide-react';
import type { Project } from '@/api/types';
import { Badge } from '@/ui/badge';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/ui/table';
import { cn } from '@/lib/cn';
import { formatDate, formatDateTime, formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';
import {
  defaultDirFor,
  isDrifted,
  isExternal,
  projectLabel,
  projectPath,
  STATUS_VARIANT,
  type ProjectSort,
  type ProjectSortKey,
} from '../lib/projectList';
import { ReindexProjectButton } from './ReindexProjectButton';
import { SyncProjectButton } from './SyncProjectButton';

interface SortHeaderProps {
  label: string;
  sortKey: ProjectSortKey;
  sort: ProjectSort;
  onSortChange: (s: ProjectSort) => void;
  className?: string;
}

function SortHeader({ label, sortKey, sort, onSortChange, className }: SortHeaderProps) {
  const active = sort.key === sortKey;
  const Icon = active ? (sort.dir === 'asc' ? ArrowUp : ArrowDown) : ChevronsUpDown;
  return (
    <TableHead className={className}>
      <button
        type="button"
        onClick={() =>
          onSortChange(
            active
              ? { key: sortKey, dir: sort.dir === 'asc' ? 'desc' : 'asc' }
              : { key: sortKey, dir: defaultDirFor(sortKey) },
          )
        }
        className="-mx-1 inline-flex items-center gap-1 rounded px-1 transition-colors hover:text-foreground"
      >
        {label}
        <Icon className={cn('h-3.5 w-3.5', active ? 'text-foreground' : 'opacity-50')} />
      </button>
    </TableHead>
  );
}

export function ProjectsTable({
  projects,
  sort,
  onSortChange,
}: {
  projects: Project[];
  sort: ProjectSort;
  onSortChange: (s: ProjectSort) => void;
}) {
  const navigate = useNavigate();
  // Drift = indexed under a different model than the sidecar is running now.
  // NULL indexed_with_model is a legacy row (pre-drift-tracking), not drift.
  const currentModel = useRuntimeModel();

  return (
    <div className="rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <SortHeader label="Name" sortKey="name" sort={sort} onSortChange={onSortChange} />
            <SortHeader label="Type" sortKey="type" sort={sort} onSortChange={onSortChange} />
            <TableHead>Status</TableHead>
            <TableHead>Languages</TableHead>
            <SortHeader
              label="Files"
              sortKey="files"
              sort={sort}
              onSortChange={onSortChange}
              className="text-right"
            />
            <SortHeader
              label="Symbols"
              sortKey="symbols"
              sort={sort}
              onSortChange={onSortChange}
              className="text-right"
            />
            <SortHeader
              label="Last indexed"
              sortKey="last_indexed"
              sort={sort}
              onSortChange={onSortChange}
            />
            <SortHeader label="Added" sortKey="created" sort={sort} onSortChange={onSortChange} />
            <TableHead className="text-right" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {projects.map((p) => {
            const external = isExternal(p);
            const drift = isDrifted(p, currentModel);
            const fullSyncRequired = !!p.full_sync_required;
            return (
              <TableRow
                key={p.path_hash}
                // Whole-row click is a convenience affordance on top of the real
                // <Link> in the name cell (which carries keyboard focus and
                // modifier-click → open-in-new-tab). Bail out when the name
                // link already handled it (defaultPrevented) or the user wants a
                // new tab/window, so we don't double-navigate or hijack it.
                onClick={(e) => {
                  if (
                    e.defaultPrevented ||
                    e.button !== 0 ||
                    e.metaKey ||
                    e.ctrlKey ||
                    e.shiftKey ||
                    e.altKey
                  )
                    return;
                  navigate(`/projects/${p.path_hash}`);
                }}
                className="cursor-pointer"
              >
                <TableCell className="max-w-[22rem] 2xl:max-w-[34rem]">
                  <Link
                    to={`/projects/${p.path_hash}`}
                    className="block truncate font-medium hover:underline focus-visible:underline focus-visible:outline-none"
                  >
                    {projectLabel(p)}
                  </Link>
                  <div className="truncate text-xs text-muted-foreground" title={projectPath(p)}>
                    {projectPath(p)}
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant={external ? 'secondary' : 'outline'} className="font-normal">
                    {external ? 'External' : 'Local'}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap items-center gap-1">
                    <Badge variant={STATUS_VARIANT[p.status]} className="capitalize">
                      {p.status}
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
                        title={p.full_sync_reason ?? undefined}
                      >
                        <AlertTriangle className="h-3 w-3" />
                        Out of sync
                      </Badge>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap items-center gap-1">
                    {p.languages.slice(0, 2).map((l) => (
                      <Badge key={l} variant="outline" className="font-normal text-xs">
                        {l}
                      </Badge>
                    ))}
                    {p.languages.length > 2 ? (
                      <span className="text-xs text-muted-foreground">
                        +{p.languages.length - 2}
                      </span>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  <span className="inline-flex items-center gap-1 text-muted-foreground">
                    <FileText className="h-3.5 w-3.5" />
                    {p.stats.indexed_files.toLocaleString()}
                  </span>
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  <span className="inline-flex items-center gap-1 text-muted-foreground">
                    <Database className="h-3.5 w-3.5" />
                    {p.stats.total_symbols.toLocaleString()}
                  </span>
                </TableCell>
                <TableCell className="whitespace-nowrap text-muted-foreground">
                  {p.last_indexed_at ? (
                    <span title={formatDateTime(p.last_indexed_at)}>
                      {formatRelative(p.last_indexed_at)}
                    </span>
                  ) : (
                    'Never'
                  )}
                </TableCell>
                <TableCell className="whitespace-nowrap text-muted-foreground">
                  <span title={formatDateTime(p.created_at)}>{formatDate(p.created_at)}</span>
                </TableCell>
                <TableCell className="text-right">
                  {external ? (
                    // The row navigates on click — intercept here so the
                    // sync/reindex actions don't also open the detail page.
                    // (Reindex opens a confirm dialog in a portal, so its
                    // clicks land outside the row anyway.)
                    <div
                      className="flex justify-end gap-1.5"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                      }}
                    >
                      <SyncProjectButton
                        hash={p.path_hash}
                        hostPath={projectPath(p)}
                        variant="outline"
                        size="sm"
                        className="h-7 px-2.5 text-xs"
                      />
                      <ReindexProjectButton
                        hash={p.path_hash}
                        hostPath={projectPath(p)}
                        variant="outline"
                        size="sm"
                        className="h-7 px-2.5 text-xs"
                      />
                    </div>
                  ) : null}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
