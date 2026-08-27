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
import { CollectionToggle } from './components/CollectionToggle';
import { StatsFiltersBar } from './components/StatsFilters';
import { StatsTable } from './components/StatsTable';
import {
  EMPTY_FILTERS,
  PAGE_SIZE,
  useDebouncedFilters,
  useResetSearchStats,
  useSearchStatsSettings,
  useSetSearchStatsSettings,
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
  const settings = useSearchStatsSettings();
  const setSettings = useSetSearchStatsSettings();
  // Resolved before the two data queries, which do not run until it is true.
  const collecting = settings.data?.enabled ?? false;

  const { data, error, isLoading } = useSearchStats(queried, collecting);
  const series = useSearchStatsSeries(queried.window, queried.kinds, collecting);
  const reset = useResetSearchStats();

  const total = data?.total ?? 0;
  useStatusFact(data ? `${total} project${total === 1 ? '' : 's'} searched` : null);

  // The server answers 503 when collection is off. That is a configuration
  // state, not a fault, so it never becomes the red "could not load" a real
  // failure earns — the toggle above explains it, and the toggle is how it is
  // changed.
  const disabled = !collecting || (error instanceof ApiError && error.status === 503);

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
        // Shown while collection is OFF too. Discarding what was collected is
        // the one action that must not require switching collection back on —
        // that would mean resuming the thing you stopped in order to clear it.
        //
        // Keyed on whether a database EXISTS, not on where the current setting
        // came from. Deriving it from the provenance ("an admin turned it off,
        // so there is probably a file") misses a server that collected under
        // CIX_SEARCH_STATS_ENABLED and then redeployed with the variable
        // flipped: nobody touched the toggle, the source still reads
        // `environment`, and the file is full.
        user?.role === 'admin' && settings.data?.has_stored_counters ? (
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
      {settings.data ? (
        <CollectionToggle
          settings={settings.data}
          canEdit={user?.role === 'admin'}
          pending={setSettings.isPending}
          error={setSettings.error}
          onChange={(next) => setSettings.mutate(next)}
        />
      ) : null}

      {settings.isLoading ? (
        <SkeletonRows rows={3} />
      ) : disabled ? (
        <Empty title="Not collecting search statistics">
          {user?.role !== 'admin'
            ? 'An admin has not turned collection on for this server, so there is nothing to show.'
            : settings.data?.has_stored_counters
              ? 'Counters collected earlier are still on disk. The table only reads them while collection is on — turn it back on above to look, or use “Clear counters” to discard them without resuming.'
              : 'Turn collection on above to start counting. Nothing has been recorded on this server yet.'}
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
