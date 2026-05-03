// Editor protocol preference. Persisted in localStorage; consumed by every
// "Open in editor" call site (currently search ResultSnippet). The default
// 'cursor' matches the prior hardcoded behaviour so users without a stored
// preference see no behaviour change.

export type EditorProtocol = 'cursor' | 'vscode' | 'none';

export const EDITOR_STORAGE_KEY = 'cix.editor.protocol';

const VALID: ReadonlySet<EditorProtocol> = new Set(['cursor', 'vscode', 'none']);

export function getEditorPreference(): EditorProtocol {
  if (typeof window === 'undefined') return 'cursor';
  try {
    const v = window.localStorage.getItem(EDITOR_STORAGE_KEY);
    if (v && VALID.has(v as EditorProtocol)) return v as EditorProtocol;
  } catch {
    /* localStorage may throw in privacy mode */
  }
  return 'cursor';
}

export function setEditorPreference(p: EditorProtocol): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(EDITOR_STORAGE_KEY, p);
  } catch {
    /* swallow — preference will reset on reload */
  }
}

// CURSOR_FALLBACK_DELAY_MS — Cursor's URL handler usually takes ~100ms to
// pull focus on macOS. We give it 250ms before falling back to VS Code,
// which is long enough that a successful Cursor handle pre-empts the
// fallback navigation, and short enough that users without Cursor barely
// see a delay.
const CURSOR_FALLBACK_DELAY_MS = 250;

export function openInEditor(absolutePath: string, line?: number): void {
  const pref = getEditorPreference();
  if (pref === 'none') return;

  const suffix = typeof line === 'number' ? `:${line}` : '';
  const cursorURL = `cursor://file/${absolutePath}${suffix}`;
  const vscodeURL = `vscode://file/${absolutePath}${suffix}`;

  if (pref === 'vscode') {
    window.location.href = vscodeURL;
    return;
  }
  // 'cursor' — try Cursor first, fall back to VS Code if no handler claims focus.
  window.location.href = cursorURL;
  window.setTimeout(() => {
    window.location.href = vscodeURL;
  }, CURSOR_FALLBACK_DELAY_MS);
}
