import { useEffect, useState } from 'react';
import { useServerStatus } from '@/lib/useServerStatus';
import { formatVersion } from '@/lib/version';

const dismissKey = (version: string) => `cix.update-dismissed.${version}`;

// A full-width ink strip above the sidebar+main row when the server-side
// version checker finds a newer `server/v*` release. Ink rather than a green
// wash: this is information, not a success state, and green is reserved for
// "the thing is alive".
//
// Dismissals are namespaced by the latest version, so dismissing 0.6.0
// silences the banner only until 0.6.1 ships.
export function UpdateBanner() {
  const { data } = useServerStatus();
  const latest = data?.latest_version ?? null;
  const updateAvailable = data?.update_available === true && !!latest;

  const [dismissed, setDismissed] = useState(false);

  // Re-evaluate the localStorage flag whenever the latest version changes —
  // a fresh release should re-show the banner even within an open tab.
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
      className="flex flex-none items-center gap-3 border-b bg-ink px-[18px] py-2 text-[13px] text-surface"
    >
      <span aria-hidden className="cix-dot is-busy" />
      <span className="min-w-0 flex-1 truncate">
        cix-server <b className="font-mono">{formatVersion(latest)}</b> is available
        {data?.server_version ? (
          <span className="text-line-quiet">
            {' '}
            — you are on <span className="font-mono">{formatVersion(data.server_version)}</span>
          </span>
        ) : null}
      </span>
      {data?.release_url ? (
        <a
          href={data.release_url}
          target="_blank"
          rel="noreferrer noopener"
          className="flex-none border-b-[1.5px] border-surface font-semibold text-surface hover:border-accent hover:text-accent"
        >
          Release notes
        </a>
      ) : null}
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss update notification"
        className="flex h-6 w-6 flex-none items-center justify-center font-mono text-surface hover:bg-surface hover:text-ink"
      >
        ✕
      </button>
    </div>
  );
}
