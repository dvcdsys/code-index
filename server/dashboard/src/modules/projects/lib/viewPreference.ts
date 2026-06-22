// Projects-page view preference (tiles vs table). Persisted in localStorage so
// the choice sticks across reloads. Mirrors the editorPreference.ts pattern.
// Default 'grid' matches the prior tiles-only behaviour — users without a
// stored preference see no change.

export type ProjectView = 'grid' | 'table';

export const PROJECT_VIEW_STORAGE_KEY = 'cix.projects.view';

const VALID: ReadonlySet<ProjectView> = new Set(['grid', 'table']);

export function getProjectView(): ProjectView {
  if (typeof window === 'undefined') return 'grid';
  try {
    const v = window.localStorage.getItem(PROJECT_VIEW_STORAGE_KEY);
    if (v && VALID.has(v as ProjectView)) return v as ProjectView;
  } catch {
    /* localStorage may throw in privacy mode */
  }
  return 'grid';
}

export function setProjectView(v: ProjectView): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(PROJECT_VIEW_STORAGE_KEY, v);
  } catch {
    /* swallow — preference will reset on reload */
  }
}
