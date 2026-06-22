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

export type ProjectSortKey = 'name' | 'last_indexed' | 'created';
export type SortDir = 'asc' | 'desc';
export interface ProjectSort {
  key: ProjectSortKey;
  dir: SortDir;
}

export type TypeFilter = 'all' | 'external' | 'local';

export interface ProjectFilter {
  search: string;
  type: TypeFilter;
}

export function filterProjects(
  projects: Project[],
  { search, type }: ProjectFilter,
): Project[] {
  const q = search.trim().toLowerCase();
  return projects.filter((p) => {
    if (type !== 'all' && isExternal(p) !== (type === 'external')) return false;
    if (q && !projectPath(p).toLowerCase().includes(q)) return false;
    return true;
  });
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
      case 'created':
        return compareTimes(a.created_at, b.created_at, factor);
      case 'last_indexed':
        return compareTimes(a.last_indexed_at, b.last_indexed_at, factor);
      default:
        return 0;
    }
  });
}

// Default sort direction when the user selects a column for the first time:
// names read best ascending (A→Z), dates most-recent-first (desc).
export function defaultDirFor(key: ProjectSortKey): SortDir {
  return key === 'name' ? 'asc' : 'desc';
}
