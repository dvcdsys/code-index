// Theme storage + system-preference resolution. Mirrors the inline script
// in index.html — keep the storage key + values in sync with that script
// (otherwise the anti-flash hint and the React state diverge on first paint).

export type ThemeMode = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

export const THEME_STORAGE_KEY = 'cix.theme';

const VALID_MODES: ReadonlySet<ThemeMode> = new Set(['light', 'dark', 'system']);

export function readStoredTheme(): ThemeMode {
  if (typeof window === 'undefined') return 'system';
  try {
    const v = window.localStorage.getItem(THEME_STORAGE_KEY);
    if (v && VALID_MODES.has(v as ThemeMode)) return v as ThemeMode;
  } catch {
    /* localStorage may throw in privacy mode */
  }
  return 'system';
}

export function writeStoredTheme(mode: ThemeMode): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, mode);
  } catch {
    /* swallow — theme will reset on reload */
  }
}

export function resolveSystemTheme(): ResolvedTheme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function resolveTheme(mode: ThemeMode): ResolvedTheme {
  return mode === 'system' ? resolveSystemTheme() : mode;
}

export function applyResolvedTheme(resolved: ResolvedTheme): void {
  if (typeof document === 'undefined') return;
  const root = document.documentElement;
  if (resolved === 'dark') root.classList.add('dark');
  else root.classList.remove('dark');
}
