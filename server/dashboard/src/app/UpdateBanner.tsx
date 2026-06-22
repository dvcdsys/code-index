import { useEffect, useState } from 'react';
import { ArrowUpCircle, X } from 'lucide-react';
import { useServerStatus } from '@/lib/useServerStatus';
import { cn } from '@/lib/cn';
import { formatVersion } from '@/lib/version';

const dismissKey = (version: string) => `cix.update-dismissed.${version}`;

// UpdateBanner renders at the top of the dashboard shell when the
// server-side version checker reports a newer `server/v*` release on
// GitHub. Dismissals are namespaced by the latest version, so dismissing
// 0.6.0 silences the banner only until 0.6.1 ships.
export function UpdateBanner() {
  const { data } = useServerStatus();
  const latest = data?.latest_version ?? null;
  const updateAvailable = data?.update_available === true && !!latest;

  const [dismissed, setDismissed] = useState(false);

  // Re-evaluate the localStorage flag whenever the latest version changes
  // — a fresh release should re-show the banner even within an open tab.
  useEffect(() => {
    if (!latest) {
      setDismissed(false);
      return;
    }
    setDismissed(localStorage.getItem(dismissKey(latest)) === '1');
  }, [latest]);

  if (!updateAvailable || dismissed || !latest) return null;

  const onDismiss = () => {
    localStorage.setItem(dismissKey(latest), '1');
    setDismissed(true);
  };

  return (
    <div
      role="status"
      className={cn(
        'flex items-center gap-3 border-b border-emerald-200 bg-emerald-50 px-5 py-2',
        'text-sm text-emerald-900',
        'dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-100',
      )}
    >
      <ArrowUpCircle className="h-4 w-4 shrink-0" aria-hidden />
      <div className="flex-1">
        cix-server <strong>{formatVersion(latest)}</strong> is available
        {data?.server_version ? (
          <> (you're running {formatVersion(data.server_version)})</>
        ) : null}
        {data?.release_url ? (
          <>
            {' — '}
            <a
              href={data.release_url}
              target="_blank"
              rel="noreferrer noopener"
              className="font-medium underline underline-offset-2 hover:no-underline"
            >
              release notes
            </a>
          </>
        ) : null}
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss update notification"
        className={cn(
          'rounded-md p-1 transition-colors',
          'hover:bg-emerald-100 dark:hover:bg-emerald-900/40',
        )}
      >
        <X className="h-4 w-4" aria-hidden />
      </button>
    </div>
  );
}
