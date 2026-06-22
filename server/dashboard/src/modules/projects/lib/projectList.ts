// Shared list logic for the Projects page — the single source of truth for how
// a project is labelled, classified (external vs local), filtered and sorted.
// Both the tiles grid (ProjectCard) and the table (ProjectsTable) import from
// here so the two views can never drift apart.

import type { Project } from '@/api/types';

export function basename(p: string): string {
  const parts = p.replace(/\/+$/, '').split('/');
  return parts[parts.length - 1] || p;
}

// Status → shadcn Badge variant. Shared so card and table badges match.
export const STATUS_VARIANT: Record<
  Project['status'],
  'default' | 'secondary' | 'destructive' | 'outline'
> = {
  created: 'outline',
  indexing: 'secondary',
  indexed: 'default',
  error: 'destructive',
};

// Labels of the two flag badges that ride alongside the lifecycle status in
// the Status column. Named constants so the badges, the filter dropdown and
// the filter predicate can never disagree on the wording.
export const STALE_MODEL_LABEL = 'Stale model';
export const OUT_OF_SYNC_LABEL = 'Out of sync';

// Capitalized lifecycle label, matching the (CSS-capitalized) status badge.
export function statusLabel(s: Project['status']): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// Drift = the project was indexed under a different embedding model than the
// sidecar is running now. NULL indexed_with_model is a legacy row (pre-drift
// tracking) — unknown, not drift. NULL currentModel (sidecar model unknown)
// means we can't judge drift, so nothing counts. Single source of truth for
// the "Stale model" badge and the status filter alike.
export function isDrifted(p: Project, currentModel: string | null): boolean {
  return !!p.indexed_with_model && !!currentModel && p.indexed_with_model !== currentModel;
}

// The full set of status labels a project shows in the Status column: its
// lifecycle status plus any active flag badges. This is what the inclusive
// status filter matches against, so the options and the predicate stay in
// lock-step with what the table actually renders.
export function projectStatuses(p: Project, currentModel: string | null): string[] {
  const out = [statusLabel(p.status)];
  if (isDrifted(p, currentModel)) out.push(STALE_MODEL_LABEL);
  if (p.full_sync_required) out.push(OUT_OF_SYNC_LABEL);
  return out;
}

// Distinct status labels present across the given projects, for the filter
// dropdown. Ordered by lifecycle (created→error) then the flag badges, and
// only labels that actually appear are returned — so the options mirror the
// current data rather than the raw server enum.
export function collectStatuses(projects: Project[], currentModel: string | null): string[] {
  const seen = new Set<string>();
  for (const p of projects) for (const s of projectStatuses(p, currentModel)) seen.add(s);
  const ORDER = [
    statusLabel('created'),
    statusLabel('indexing'),
    statusLabel('indexed'),
    statusLabel('error'),
    STALE_MODEL_LABEL,
    OUT_OF_SYNC_LABEL,
  ];
  return ORDER.filter((s) => seen.has(s));
}

// External = GitHub-cloned project the server can pull + incrementally index.
// We classify on the host_path prefix rather than owner_user_id to stay
// consistent with the existing Sync-button gating. This is safe because local
// host_paths are absolute filesystem paths (or `local:`-namespaced) and can
// never begin with `github.com/`.
//
// This is the single source of truth for the whole dashboard — ProjectCard,
// ProjectsTable, ProjectDetailPage and WorkspaceProjectRow all call it, so the
// definition can't drift. The param is structural (anything with host_path) so
// the leaner WorkspaceProject.project shape can use it too.
export function isExternal(p: { host_path: string }): boolean {
  return p.host_path.startsWith('github.com/');
}

// Human-readable label the list shows: the basename of the display path.
export function projectLabel(p: Project): string {
  return basename(p.display_path ?? p.host_path);
}

// Full path used for the muted secondary line and the name-search match.
export function projectPath(p: Project): string {
  return p.display_path ?? p.host_path;
}

export type ProjectSortKey =
  | 'name'
  | 'type'
  | 'files'
  | 'symbols'
  | 'last_indexed'
  | 'created';
export type SortDir = 'asc' | 'desc';
export interface ProjectSort {
  key: ProjectSortKey;
  dir: SortDir;
}

export type TypeFilter = 'all' | 'external' | 'local';

export interface ProjectFilter {
  search: string;
  type: TypeFilter;
  // Inclusive status filter: the selected status labels (from the Status
  // column's badge set). Empty = no status constraint. A project matches when
  // its status set contains EVERY selected label — so {Indexed} keeps every
  // indexed project, and {Indexed, Out of sync} narrows to those that are both.
  statuses: string[];
  // Sidecar embedding model, needed to resolve the "Stale model" status the
  // same way the badges do. NULL when unknown.
  currentModel: string | null;
  // A single language tag to match, or 'all'. Matches when the project lists
  // that language (case-sensitive — the tags come straight from the indexer).
  language: string;
}

export function filterProjects(
  projects: Project[],
  { search, type, statuses, currentModel, language }: ProjectFilter,
): Project[] {
  const q = search.trim().toLowerCase();
  return projects.filter((p) => {
    if (type !== 'all' && isExternal(p) !== (type === 'external')) return false;
    if (statuses.length > 0) {
      const have = projectStatuses(p, currentModel);
      if (!statuses.every((s) => have.includes(s))) return false;
    }
    if (language !== 'all' && !p.languages.includes(language)) return false;
    if (q && !projectPath(p).toLowerCase().includes(q)) return false;
    return true;
  });
}

// Distinct languages across the given projects, case-insensitively sorted, for
// populating the Languages filter dropdown. Derived from the full project list
// (not the filtered view) so the available options stay stable as the user
// narrows down.
export function collectLanguages(projects: Project[]): string[] {
  const seen = new Set<string>();
  for (const p of projects) for (const l of p.languages) seen.add(l);
  return [...seen].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
}

// Parse a nullable timestamp to epoch millis; null/invalid → null so callers
// can keep never-indexed rows at the bottom regardless of sort direction.
function timeMs(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? null : t;
}

// Compare two nullable timestamps. Missing/invalid values always sink to the
// bottom irrespective of `factor` — they have no meaningful position in the
// order (e.g. a never-indexed project under the "Last indexed" sort). Used for
// both date columns so they behave identically.
function compareTimes(
  a: string | null | undefined,
  b: string | null | undefined,
  factor: number,
): number {
  const ta = timeMs(a);
  const tb = timeMs(b);
  if (ta === null && tb === null) return 0;
  if (ta === null) return 1;
  if (tb === null) return -1;
  return (ta - tb) * factor;
}

export function sortProjects(projects: Project[], sort: ProjectSort): Project[] {
  const factor = sort.dir === 'asc' ? 1 : -1;
  // Copy first — never mutate the React Query cache array in place.
  return [...projects].sort((a, b) => {
    switch (sort.key) {
      case 'name':
        // sensitivity:'base' → case- and accent-insensitive, so "Zebra" and
        // "apple" order by letter rather than by ASCII case.
        return (
          projectLabel(a).localeCompare(projectLabel(b), undefined, {
            sensitivity: 'base',
          }) * factor
        );
      case 'type':
        // Local (false) before External (true) ascending. Equal types keep
        // their incoming order (stable sort) — no secondary key needed.
        return ((isExternal(a) ? 1 : 0) - (isExternal(b) ? 1 : 0)) * factor;
      case 'files':
        return (a.stats.indexed_files - b.stats.indexed_files) * factor;
      case 'symbols':
        return (a.stats.total_symbols - b.stats.total_symbols) * factor;
      case 'created':
        return compareTimes(a.created_at, b.created_at, factor);
      case 'last_indexed':
        return compareTimes(a.last_indexed_at, b.last_indexed_at, factor);
      default:
        return 0;
    }
  });
}

// Default sort direction when the user selects a column for the first time.
// Text/lifecycle columns read best ascending (A→Z, created→error); count and
// date columns most-impactful-first, i.e. descending (most files/symbols/
// languages, most-recently-indexed/added).
const ASC_FIRST: ReadonlySet<ProjectSortKey> = new Set(['name', 'type']);
export function defaultDirFor(key: ProjectSortKey): SortDir {
  return ASC_FIRST.has(key) ? 'asc' : 'desc';
}
