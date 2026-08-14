import { Link, useNavigate } from 'react-router-dom';
import type { Project } from '@/api/types';
import { Badge, Status } from '@/ui/badge';
import { Table, TBody, THead, TR, TH, TD } from '@/ui/table';
import { cn } from '@/lib/cn';
import { formatDate, formatDateTime, formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';
import {
  defaultDirFor,
  isDrifted,
  isExternal,
  projectLabel,
  projectPath,
  STATUS_TONE,
  statusLabel,
  type ProjectSort,
  type ProjectSortKey,
} from '../lib/projectList';
import { ReindexProjectButton } from './ReindexProjectButton';
import { SyncProjectButton } from './SyncProjectButton';

// Sort direction is a mono caret, not an icon set: ▲ / ▼ when active, a faint
// ↕ otherwise. Numeric columns keep their right alignment in the header too,
// so the arrow sits on the same axis as the digits below it.
function SortHeader({
  label,
  sortKey,
  sort,
  onSortChange,
  align,
}: {
  label: string;
  sortKey: ProjectSortKey;
  sort: ProjectSort;
  onSortChange: (s: ProjectSort) => void;
  align?: 'left' | 'right';
}) {
  const active = sort.key === sortKey;
  const glyph = active ? (sort.dir === 'asc' ? '▲' : '▼') : '↕';
  return (
    <TH align={align}>
      <button
        type="button"
        aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
        onClick={() =>
          onSortChange(
            active
              ? { key: sortKey, dir: sort.dir === 'asc' ? 'desc' : 'asc' }
              : { key: sortKey, dir: defaultDirFor(sortKey) }
          )
        }
        className="inline-flex items-center gap-1.5 uppercase tracking-[0.14em] hover:text-ink"
      >
        {label}
        <span aria-hidden className={cn('text-[9px]', active ? 'text-accent' : 'text-faint')}>
          {glyph}
        </span>
      </button>
    </TH>
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
  // Drift = indexed under a different model than the sidecar runs now. A NULL
  // indexed_with_model is a legacy row (pre-drift-tracking), not drift.
  const currentModel = useRuntimeModel();

  return (
    <Table card>
      <THead>
        <TR>
          <SortHeader label="Project" sortKey="name" sort={sort} onSortChange={onSortChange} />
          <SortHeader label="Type" sortKey="type" sort={sort} onSortChange={onSortChange} />
          <TH>Status</TH>
          <TH>Languages</TH>
          <SortHeader
            label="Files"
            sortKey="files"
            sort={sort}
            onSortChange={onSortChange}
            align="right"
          />
          <SortHeader
            label="Symbols"
            sortKey="symbols"
            sort={sort}
            onSortChange={onSortChange}
            align="right"
          />
          <SortHeader
            label="Last indexed"
            sortKey="last_indexed"
            sort={sort}
            onSortChange={onSortChange}
          />
          <SortHeader label="Added" sortKey="created" sort={sort} onSortChange={onSortChange} />
          <TH align="right" />
        </TR>
      </THead>
      <TBody>
        {projects.map((p) => {
          const external = isExternal(p);
          const drift = isDrifted(p, currentModel);
          return (
            <TR
              key={p.path_hash}
              // Whole-row click is a convenience on top of the real <Link> in
              // the name cell (which owns keyboard focus and modifier-click →
              // new tab). Bail when the link already handled it, or when the
              // user asked for a new tab, so we never double-navigate.
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
              <TD className="max-w-[22rem] 2xl:max-w-[34rem]">
                <Link
                  to={`/projects/${p.path_hash}`}
                  className="block truncate font-semibold text-ink hover:text-accent"
                >
                  {projectLabel(p)}
                </Link>
                <div className="cix-row__meta truncate" title={projectPath(p)}>
                  {projectPath(p)}
                </div>
              </TD>
              <TD>
                <Badge variant={external ? 'outline' : 'quiet'}>
                  {external ? 'external' : 'local'}
                </Badge>
              </TD>
              <TD>
                <div className="flex flex-wrap items-center gap-1.5">
                  <Status tone={STATUS_TONE[p.status]} className="font-mono text-[11.5px]">
                    {statusLabel(p.status).toLowerCase()}
                  </Status>
                  {drift ? <Badge variant="warn">stale model</Badge> : null}
                  {p.full_sync_required ? (
                    <Badge variant="busy" title={p.full_sync_reason ?? undefined}>
                      out of sync
                    </Badge>
                  ) : null}
                </div>
              </TD>
              <TD>
                <div className="flex flex-wrap items-center gap-1.5">
                  {p.languages.slice(0, 2).map((l) => (
                    <Badge key={l} variant="quiet">
                      {l}
                    </Badge>
                  ))}
                  {p.languages.length > 2 ? (
                    <span className="cix-row__meta">+{p.languages.length - 2}</span>
                  ) : null}
                </div>
              </TD>
              <TD mono align="right" className="tabular-nums">
                {p.stats.indexed_files.toLocaleString()}
              </TD>
              <TD mono align="right" className="tabular-nums">
                {p.stats.total_symbols.toLocaleString()}
              </TD>
              <TD mono className="whitespace-nowrap">
                {p.last_indexed_at ? (
                  <span title={formatDateTime(p.last_indexed_at)}>
                    {formatRelative(p.last_indexed_at)}
                  </span>
                ) : (
                  <span className="text-faint">never</span>
                )}
              </TD>
              <TD mono className="whitespace-nowrap" title={formatDateTime(p.created_at)}>
                {formatDate(p.created_at)}
              </TD>
              <TD align="right">
                {external ? (
                  // The row navigates on click — stop these so the sync /
                  // reindex actions don't also open the detail page. (Reindex
                  // confirms in a portal, so its own clicks land outside.)
                  <div
                    className="flex justify-end gap-1.5"
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                    }}
                  >
                    <SyncProjectButton hash={p.path_hash} hostPath={projectPath(p)} size="sm" />
                    <ReindexProjectButton hash={p.path_hash} hostPath={projectPath(p)} size="sm" />
                  </div>
                ) : null}
              </TD>
            </TR>
          );
        })}
      </TBody>
    </Table>
  );
}
