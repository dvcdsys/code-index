import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { SearchStatsResponse, SearchStatsSeriesResponse } from '@/api/types';

// Every window the server will serve. `all` reads the cumulative totals; the
// rest read the 30-minute-bucket tier, which is retained for seven days — so
// there is deliberately no "30 days" option to offer, because there would be
// nothing behind it.
export const WINDOWS = [
  { value: 'all', label: 'All time' },
  { value: '7d', label: 'Last 7 days' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '1h', label: 'Last hour' },
] as const;

export type StatsWindow = (typeof WINDOWS)[number]['value'];

export const KINDS = [
  { value: 'semantic', label: 'Semantic' },
  { value: 'workspace', label: 'Workspace' },
  { value: 'symbols', label: 'Symbols' },
  { value: 'definitions', label: 'Definitions' },
  { value: 'references', label: 'References' },
  { value: 'files', label: 'Files' },
] as const;

export const SORTS = [
  { value: 'queries', label: 'Queries' },
  { value: 'project', label: 'Project' },
  { value: 'top_file_hits', label: 'Top file' },
  { value: 'file_hits', label: 'File hits' },
  { value: 'distinct_files', label: 'Distinct files' },
  { value: 'last_seen', label: 'Last searched' },
] as const;

export type StatsSort = (typeof SORTS)[number]['value'];

export const PAGE_SIZE = 25;

export interface StatsFilters {
  window: StatsWindow;
  kinds: string[];
  project: string;
  minQueries: string;
  maxQueries: string;
  minTopFileHits: string;
  maxTopFileHits: string;
  sort: StatsSort;
  desc: boolean;
  page: number;
}

export const EMPTY_FILTERS: StatsFilters = {
  window: 'all',
  kinds: [],
  project: '',
  minQueries: '',
  maxQueries: '',
  minTopFileHits: '',
  maxTopFileHits: '',
  sort: 'queries',
  desc: true,
  page: 0,
};

// The numeric filters are held as STRINGS in component state, not as numbers.
// A number would have to represent "the field is empty" as something, and every
// candidate is a real filter value the user did not ask for: 0 filters out the
// projects with no searches, and NaN has to be special-cased at every use.
// Empty stays empty, and only a value that parses becomes a parameter.
//
// This says nothing about WHEN the request goes out — a half-typed "1" on the
// way to "13" parses perfectly well. That is what the debounce below is for.
function num(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (trimmed === '') return undefined;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return String(Math.floor(n));
}

export function toQuery(f: StatsFilters): string {
  const q = new URLSearchParams();
  q.set('window', f.window);
  q.set('sort', f.sort);
  q.set('order', f.desc ? 'desc' : 'asc');
  q.set('limit', String(PAGE_SIZE));
  q.set('offset', String(f.page * PAGE_SIZE));
  q.set('top_files', '5');
  if (f.kinds.length > 0) q.set('kinds', f.kinds.join(','));
  if (f.project.trim() !== '') q.set('project', f.project.trim());
  const ranges: Array<[string, string]> = [
    ['min_queries', f.minQueries],
    ['max_queries', f.maxQueries],
    ['min_top_file_hits', f.minTopFileHits],
    ['max_top_file_hits', f.maxTopFileHits],
  ];
  for (const [key, raw] of ranges) {
    const v = num(raw);
    if (v !== undefined) q.set(key, v);
  }
  return q.toString();
}

// Fields typed one character at a time. Changing one of these waits; changing
// anything else — a window, a sort, a kind chip — applies at once, because a
// dropdown that lags behind the click reads as a broken control.
const DEBOUNCED_FIELDS = [
  'project',
  'minQueries',
  'maxQueries',
  'minTopFileHits',
  'maxTopFileHits',
] as const satisfies ReadonlyArray<keyof StatsFilters>;

const DEBOUNCE_MS = 300;

// A single string rather than an object so the effect below has a primitive to
// depend on. An object rebuilt every render would restart the timer on every
// render, and the request would never fire while anything else on the page
// re-rendered.
function textKey(f: StatsFilters): string {
  return DEBOUNCED_FIELDS.map((k) => f[k]).join('\u0000');
}

// Returns the filters to actually query with: everything as typed, except the
// free-text fields, which lag by DEBOUNCE_MS. Typing "150" therefore issues one
// request rather than three.
export function useDebouncedFilters(filters: StatsFilters): StatsFilters {
  const key = textKey(filters);
  const [settled, setSettled] = useState(key);

  useEffect(() => {
    if (key === settled) return;
    const t = setTimeout(() => setSettled(key), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [key, settled]);

  return useMemo(() => {
    const parts = settled.split('\u0000');
    const out = { ...filters };
    DEBOUNCED_FIELDS.forEach((field, i) => {
      out[field] = parts[i] ?? '';
    });
    return out;
    // `filters` carries the fields that are NOT debounced, so it belongs in the
    // dependencies; `settled` supplies the rest.
  }, [filters, settled]);
}

export const searchStatsKeys = {
  table: (query: string) => ['search-stats', 'table', query] as const,
  series: (query: string) => ['search-stats', 'series', query] as const,
};

export function useSearchStats(filters: StatsFilters) {
  const query = toQuery(filters);
  return useQuery({
    queryKey: searchStatsKeys.table(query),
    queryFn: ({ signal }) =>
      api.get<SearchStatsResponse>(`/search-stats?${query}`, { signal }),
    // Counters are flushed in batches every few seconds, so a page held open
    // goes stale quietly. Thirty seconds is far longer than the flush interval
    // and short enough that the table is not lying by the time anyone reads it.
    refetchInterval: 30_000,
    // Filtering should not blank the table while the next page loads —
    // the previous rows stay until the new ones arrive.
    placeholderData: (prev) => prev,
  });
}

export function useSearchStatsSeries(window: StatsWindow, kinds: string[]) {
  // `all` has no series behind it: the cumulative tier carries no buckets.
  // Fall back to the widest window that does.
  const effective = window === 'all' ? '7d' : window;
  const q = new URLSearchParams({ window: effective });
  if (kinds.length > 0) q.set('kinds', kinds.join(','));
  const query = q.toString();
  return useQuery({
    queryKey: searchStatsKeys.series(query),
    queryFn: ({ signal }) =>
      api.get<SearchStatsSeriesResponse>(`/search-stats/series?${query}`, { signal }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });
}

export function useResetSearchStats() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<void>('/admin/search-stats/reset', {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['search-stats'] }),
  });
}
