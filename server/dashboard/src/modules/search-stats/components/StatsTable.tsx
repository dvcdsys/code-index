import type { SearchStatsProject } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Table, TBody, THead, TR, TH, TD } from '@/ui/table';
import { formatRelative } from '@/lib/formatDate';
import { cn } from '@/lib/cn';
import type { StatsSort } from '../hooks';

// A header cell that also sorts. Clicking the active column flips the
// direction; clicking another switches to it and starts descending, because
// every column here is a counter and the interesting end of one is the top.
function SortTH({
  column,
  label,
  sort,
  desc,
  onSort,
  align,
  className,
}: {
  column: StatsSort;
  label: string;
  sort: StatsSort;
  desc: boolean;
  onSort: (column: StatsSort) => void;
  align?: 'left' | 'right';
  className?: string;
}) {
  const active = sort === column;
  return (
    <TH align={align} className={className} aria-sort={active ? (desc ? 'descending' : 'ascending') : 'none'}>
      <button
        type="button"
        onClick={() => onSort(column)}
        className={cn(
          'inline-flex items-center gap-1 font-inherit uppercase tracking-inherit',
          'hover:text-ink',
          active ? 'text-ink' : 'text-muted'
        )}
      >
        {label}
        {/* The marker is rendered only for the active column: an arrow on
            every header tells the reader nothing about which one is sorting. */}
        <span aria-hidden className={cn('font-mono text-[10px]', !active && 'opacity-0')}>
          {desc ? '↓' : '↑'}
        </span>
      </button>
    </TH>
  );
}

function isoFromUnix(seconds: number): string | null {
  if (!seconds) return null;
  return new Date(seconds * 1000).toISOString();
}

export function StatsTable({
  rows,
  sort,
  desc,
  onSort,
}: {
  rows: SearchStatsProject[];
  sort: StatsSort;
  desc: boolean;
  onSort: (column: StatsSort) => void;
}) {
  return (
    <Table card>
      <THead>
        <TR>
          <SortTH column="project" label="Project" sort={sort} desc={desc} onSort={onSort} />
          <SortTH
            column="queries"
            label="Queries"
            sort={sort}
            desc={desc}
            onSort={onSort}
            align="right"
            className="w-[100px]"
          />
          <SortTH
            column="distinct_files"
            label="Files"
            sort={sort}
            desc={desc}
            onSort={onSort}
            align="right"
            className="w-[90px]"
          />
          <SortTH
            column="top_file_hits"
            label="Top files in results"
            sort={sort}
            desc={desc}
            onSort={onSort}
            className="w-[46%]"
          />
          <SortTH
            column="last_seen"
            label="Last searched"
            sort={sort}
            desc={desc}
            onSort={onSort}
            className="w-[140px]"
          />
        </TR>
      </THead>
      <TBody>
        {rows.map((row) => (
          <TR key={row.project_path}>
            <TD>
              <span className="flex min-w-0 items-center gap-2">
                <span className="min-w-0 truncate font-semibold" title={row.project_path}>
                  {row.name || row.project_path}
                </span>
                {row.kind === 'external' ? (
                  <Badge variant="outline" title="Cloned from GitHub by the server">
                    external
                  </Badge>
                ) : null}
              </span>
            </TD>
            <TD mono align="right" className="tabular-nums">
              {row.queries.toLocaleString()}
            </TD>
            <TD
              mono
              align="right"
              className="tabular-nums text-dim"
              title={`${row.file_hits.toLocaleString()} total file appearances`}
            >
              {row.distinct_files.toLocaleString()}
            </TD>
            <TD>
              {row.top_files.length === 0 ? (
                <span className="font-mono text-[11.5px] text-muted">
                  no files returned
                </span>
              ) : (
                <ul className="m-0 flex list-none flex-col gap-0.5 p-0">
                  {row.top_files.map((f) => (
                    <li key={f.file_path} className="flex items-baseline gap-2">
                      {/* The bar is the comparison; the number is the fact.
                          Width is relative to the project's OWN top file, so a
                          quiet project's shape is still readable next to a busy
                          one rather than collapsing to a sliver. */}
                      <span
                        aria-hidden
                        className="h-[3px] flex-none bg-ink/25"
                        style={{
                          width: `${Math.max(4, Math.round((f.hits / Math.max(1, row.top_file_hits)) * 44))}px`,
                        }}
                      />
                      <span
                        className="min-w-0 flex-1 truncate font-mono text-[11.5px]"
                        title={f.file_path}
                      >
                        {f.file_path}
                      </span>
                      <span
                        className="flex-none font-mono text-[11.5px] tabular-nums text-dim"
                        title={`Appeared in ${f.hits} of this project's ${row.queries} searches`}
                      >
                        {f.hits.toLocaleString()}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </TD>
            <TD mono className="text-dim">
              {formatRelative(isoFromUnix(row.last_seen))}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
