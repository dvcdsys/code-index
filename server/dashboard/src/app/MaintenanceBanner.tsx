import { useEffect, useRef, useState } from 'react';
import { isActivePhase, type MaintenanceOperation } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';

// How long a finished operation stays on screen. Long enough to be read after
// the restart it caused, short enough not to become furniture.
const TERMINAL_LINGER_MS = 60_000;

// How recently an operation must have finished for its outcome to be news.
//
// The journal is never cleared — it is the only record of an operation whose
// result could not be written to the database it replaced — so without this
// every page load for the rest of the server's life would replay the same
// "database compacted" strip, and a nightly reclaim would replay it nightly.
const TERMINAL_FRESH_MS = 5 * 60_000;

const POLL_ACTIVE_MS = 2_000;
const POLL_IDLE_MS = 30_000;

type Status =
  | { kind: 'idle' }
  | { kind: 'op'; op: MaintenanceOperation }
  | { kind: 'unreachable' };

// The endpoint is deliberately outside /api/v1 and outside auth, so it is
// fetched directly rather than through the API client: it has to answer while
// sessions cannot be written, and the client would prefix it wrongly anyway.
//
// A non-2xx throws rather than resolving to "unreachable": the caller decides
// what a failure means, and it only means "restarting" if we had seen an
// operation first. A proxy answering 404 for a route it does not know about is
// not a compaction in progress.
async function fetchStatus(signal: AbortSignal): Promise<Status> {
  const res = await fetch('/maintenance/status', { credentials: 'same-origin', signal });
  if (!res.ok) throw new Error(`maintenance status: ${res.status}`);
  const op = (await res.json()) as MaintenanceOperation;
  if (!op || op.phase === 'idle') return { kind: 'idle' };
  return { kind: 'op', op };
}

// Whether a finished operation is recent enough to still be worth showing.
// An operation with no finish time is a live one and is handled elsewhere.
function isFresh(op: MaintenanceOperation): boolean {
  if (!op.finished_at) return false;
  const at = Date.parse(op.finished_at);
  if (Number.isNaN(at)) return false;
  return Date.now() - at < TERMINAL_FRESH_MS;
}

// A full-width strip reporting database compaction on every page.
//
// It owns its polling rather than going through react-query because it has a
// requirement nothing else in the dashboard has: it must keep working while
// its own backend goes away. Compaction restarts the server, so a fetch
// failure here is the *expected* middle of the operation, not an error —
// react-query's default two retries would give up and the cached "ok" would
// linger, showing nothing at exactly the moment the user wants to know what is
// happening. So a failed poll renders "reconnecting" and the interval stays
// short until the server answers again.
export function MaintenanceBanner() {
  const [status, setStatus] = useState<Status>({ kind: 'idle' });
  // Remembering that we saw an operation is what turns a connection failure
  // into "reconnecting" rather than silence: without it, a banner that was up
  // would vanish the instant the server restarted.
  const sawOperation = useRef(false);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();

    const poll = async () => {
      let next: Status;
      try {
        next = await fetchStatus(controller.signal);
      } catch {
        // The server is restarting into the compacted database, or is simply
        // down. Either way this is not an error to report.
        next = sawOperation.current ? { kind: 'unreachable' } : { kind: 'idle' };
      }
      if (cancelled) return;
      if (next.kind === 'op') sawOperation.current = true;
      setStatus(next);

      const active =
        next.kind === 'unreachable' || (next.kind === 'op' && isActivePhase(next.op.phase));
      timer = setTimeout(poll, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
    };

    void poll();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, []);

  if (status.kind === 'idle') return null;

  if (status.kind === 'unreachable') {
    return (
      <Strip busy>
        The server is restarting to adopt the compacted database — reconnecting…
      </Strip>
    );
  }

  const { op } = status;

  if (!isActivePhase(op.phase)) {
    // Terminal. Show the outcome of an operation that outlived its own
    // process — but only while it is still news. The journal keeps its last
    // entry forever by design, and a permanent banner is not a report.
    if (!isFresh(op)) return null;
    return <TerminalStrip op={op} />;
  }

  const pct =
    op.bytes_total && op.bytes_total > 0 && op.bytes_done
      ? Math.min(100, Math.round((op.bytes_done / op.bytes_total) * 100))
      : null;

  return (
    <Strip busy>
      <b>Compacting the database</b>
      {op.message ? <span className="text-line-quiet"> — {op.message}</span> : null}
      {pct !== null ? (
        <span className="text-line-quiet">
          {' '}
          · {pct}% ({formatBytes(op.bytes_done ?? 0)} of {formatBytes(op.bytes_total ?? 0)})
        </span>
      ) : null}
    </Strip>
  );
}

function TerminalStrip({ op }: { op: MaintenanceOperation }) {
  const [hidden, setHidden] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setHidden(true), TERMINAL_LINGER_MS);
    return () => clearTimeout(t);
  }, [op.run_id]);
  if (hidden) return null;

  // A scheduled reclaim journals its outcome here too, and calling that a
  // compaction would tell an admin their server had been restarted when it
  // never left service.
  const what = op.kind === 'reclaim' ? 'reclaim' : 'compaction';

  if (op.phase === 'failed') {
    return (
      <Strip>
        <b>Database {what} failed</b>
        <span className="text-line-quiet"> — {op.error ?? 'see the server log'}</span>
        <span className="text-line-quiet"> · the database was not modified</span>
      </Strip>
    );
  }
  if (op.phase === 'interrupted') {
    return (
      <Strip>
        <b>A database {what} did not finish</b>
        {op.message ? <span className="text-line-quiet"> — {op.message}</span> : null}
      </Strip>
    );
  }
  return (
    <Strip>
      <b>{op.kind === 'reclaim' ? 'Database space reclaimed' : 'Database compacted'}</b>
      {op.freed_bytes ? (
        <span className="text-line-quiet"> — {formatBytes(op.freed_bytes)} returned to the filesystem</span>
      ) : null}
    </Strip>
  );
}

function Strip({ children, busy }: { children: React.ReactNode; busy?: boolean }) {
  return (
    <div
      role="status"
      className="flex flex-none items-center gap-3 border-b bg-ink px-[18px] py-2 text-[13px] text-surface"
    >
      <span aria-hidden className={busy ? 'cix-dot is-busy' : 'cix-dot'} />
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </div>
  );
}
