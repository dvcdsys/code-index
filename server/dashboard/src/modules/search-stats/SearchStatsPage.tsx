import { useState } from 'react';
import { ApiError } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Empty, StatStrip } from '@/ui/card';
import { Page, SectionLabel } from '@/ui/page';
import { SkeletonRows } from '@/ui/skeleton';
import { TableNote } from '@/ui/table';
import { ActivityChart } from './components/ActivityChart';
import { StatsFiltersBar } from './components/StatsFilters';
import { StatsTable } from './components/StatsTable';
import {
  EMPTY_FILTERS,
  PAGE_SIZE,
  useDebouncedFilters,
  useResetSearchStats,
  useSearchStats,
  useSearchStatsSeries,
  type StatsFilters,
  type StatsSort,
} from './hooks';

export default function SearchStatsPage() {
  const { user } = useAuth();
  const [filters, setFilters] = useState<StatsFilters>(EMPTY_FILTERS);
  // The inputs render from `filters` so typing stays instant; the requests go
  // out from the debounced copy.
  const queried = useDebouncedFilters(filters);
  const { data, error, isLoading } = useSearchStats(queried);
  const series = useSearchStatsSeries(queried.window, queried.kinds);
  const reset = useResetSearchStats();

  const total = data?.total ?? 0;
  useStatusFact(data ? `${total} project${total === 1 ? '' : 's'} searched` : null);

  // The server answers 503 when CIX_SEARCH_STATS_ENABLED is off. That is a
  // configuration state, not a fault, so it gets its own explanation instead
  // of the red "could not load" that a real failure earns.
  const disabled = error instanceof ApiError && error.status === 503;

  const onSort = (column: StatsSort) => {
    setFilters((f) =>
      f.sort === column ? { ...f, desc: !f.desc, page: 0 } : { ...f, sort: column, desc: true, page: 0 }
    );
  };

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const windowLabel = filters.window === 'all' ? 'all time' : `the last ${filters.window}`;

  return (
    <Page
      title="Search statistics"
      subtitle="How often each project is searched, and which of its files keep coming back in the results. Counters only — no query text is recorded anywhere."
      action={
        user?.role === 'admin' && !disabled ? (
          <Button
            variant="quietDanger"
            size="sm"
            disabled={reset.isPending}
            onClick={() => {
              if (window.confirm('Discard every recorded search counter? This cannot be undone.')) {
                reset.mutate();
              }
            }}
          >
            {reset.isPending ? 'Clearing…' : 'Clear counters'}
          </Button>
        ) : null
      }
    >
      {disabled ? (
        <Empty title="Search statistics are switched off">
          This server runs with <code>CIX_SEARCH_STATS_ENABLED=false</code>, so no counters are
          recorded and none are kept. Set it to <code>true</code> and restart to start collecting.
        </Empty>
      ) : (
        <>
          <StatsFiltersBar filters={filters} onChange={setFilters} />

          {data ? (
            <div className="mb-4">
              <StatStrip
                items={[
                  {
                    label: 'Searches',
                    value: (data.totals?.queries ?? 0).toLocaleString(),
                    title: `Total searches across every project you can see, in ${windowLabel}`,
                  },
                  {
                    label: 'Files returned',
                    value: (data.totals?.results ?? 0).toLocaleString(),
                    title: 'Total file appearances across those searches',
                  },
                  { label: 'Projects searched', value: total.toLocaleString() },
                  {
                    label: 'Never searched',
                    value: (data.projects_without_activity ?? 0).toLocaleString(),
                    title: 'Projects you can see that have never appeared in a search',
                  },
                ]}
              />
            </div>
          ) : null}

          <div className="cix-card mb-4 p-[18px]">
            <SectionLabel
              aside={
                <span className="font-mono text-[11px] text-muted">
                  {filters.window === 'all' ? 'last 7 days' : `last ${filters.window}`}
                </span>
              }
            >
              Activity
            </SectionLabel>
            {series.isLoading ? (
              <SkeletonRows rows={1} />
            ) : series.data ? (
              <ActivityChart data={series.data} />
            ) : (
              <p className="m-0 font-mono text-[11.5px] text-muted">Activity unavailable.</p>
            )}
          </div>

          {isLoading ? (
            <SkeletonRows rows={5} />
          ) : error ? (
            <Callout variant="danger">
              <b>Could not load search statistics</b>
              <p>{error instanceof ApiError ? error.detail : String(error)}</p>
            </Callout>
          ) : !data || data.projects.length === 0 ? (
            <Empty title="Nothing recorded yet">
              No project matches these filters in {windowLabel}. Counters appear here once someone
              runs a search — from the CLI, the dashboard, or an editor.
            </Empty>
          ) : (
            <>
              <StatsTable
                rows={data.projects}
                sort={filters.sort}
                desc={filters.desc}
                onSort={onSort}
              />
              <TableNote
                left={`${total} project${total === 1 ? '' : 's'} · showing ${data.projects.length}`}
                right={
                  pages > 1 ? (
                    <span className="inline-flex items-center gap-2">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={filters.page === 0}
                        onClick={() => setFilters((f) => ({ ...f, page: f.page - 1 }))}
                      >
                        ← prev
                      </Button>
                      <span>
                        page {filters.page + 1} of {pages}
                      </span>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={filters.page + 1 >= pages}
                        onClick={() => setFilters((f) => ({ ...f, page: f.page + 1 }))}
                      >
                        next →
                      </Button>
                    </span>
                  ) : (
                    'a file counts once per search, however many of its chunks matched'
                  )
                }
              />
            </>
          )}
        </>
      )}
    </Page>
  );
}
