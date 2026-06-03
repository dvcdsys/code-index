import { useCallback, useRef, useState } from 'react';
import { toast } from 'sonner';

// Last-resort copy when the async Clipboard API isn't available — happens on
// plain HTTP deploys (non-localhost) and inside some embedded webviews.
// document.execCommand('copy') is deprecated but universally implemented as of
// 2026; keeping it as a fallback turns "no way to copy" into "always works".
function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}

// useCopy — one-click clipboard with a transient "copied" flag and graceful
// fallback. navigator.clipboard requires a secure context (HTTPS or localhost);
// on bare-IP / HTTP deploys it throws, so we fall back to document.execCommand
// through a transient textarea. On total failure we surface a toast telling the
// user to copy manually. `copied` flips true for `resetMs` after a success so
// callers can show a checkmark without re-implementing the timer.
export function useCopy(resetMs = 2000): {
  copied: boolean;
  copy: (text: string) => Promise<boolean>;
} {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | null>(null);

  const copy = useCallback(
    async (text: string): Promise<boolean> => {
      let ok = false;
      try {
        if (window.isSecureContext && navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(text);
          ok = true;
        } else {
          ok = legacyCopy(text);
          if (!ok) throw new Error('legacy copy failed');
        }
      } catch {
        toast.error('Could not copy automatically.', {
          description: 'Select the text, ⌘A / Ctrl-A, then copy.',
        });
        return false;
      }
      setCopied(true);
      if (timer.current) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), resetMs);
      return ok;
    },
    [resetMs]
  );

  return { copied, copy };
}
