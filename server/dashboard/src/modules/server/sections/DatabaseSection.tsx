import { useState } from 'react';
import { ApiError } from '@/api/client';
import { isActivePhase } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';
import { toast } from 'sonner';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Card, CardBody, CardFoot, CardHead, KV, StatStrip } from '@/ui/card';
import { Skeleton } from '@/ui/skeleton';
import { SwitchRow } from '@/ui/switch';
import { ConfirmCompactDialog } from '../components/ConfirmCompactDialog';
import { ScheduleRow } from '../components/ScheduleRow';
import {
  useCompactDatabase,
  useDatabaseState,
  useReclaimFreePages,
  useSchedules,
  useSetAutoVacuum,
} from '../hooks';

const VERDICT_VARIANT = {
  ok: 'default',
  recommended: 'warn',
  urgent: 'danger',
} as const;

export function DatabaseSection() {
  const state = useDatabaseState();
  const compact = useCompactDatabase();
  const reclaim = useReclaimFreePages();
  const setMode = useSetAutoVacuum();
  const schedules = useSchedules();

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [toggleTo, setToggleTo] = useState<'incremental' | 'none' | null>(null);

  const db = state.data;
  const op = db?.operation ?? null;
  const running = isActivePhase(op?.phase);
  const busy = running || compact.isPending || reclaim.isPending || setMode.isPending;

  const describe = (err: unknown) => (err instanceof ApiError ? err.detail : String(err));

  const started = {
    onSuccess: () => {
      setConfirmOpen(false);
      toast.success('Rebuild started', {
        description: 'The server is read-only until it finishes, then it restarts itself.',
      });
    },
    onError: (err: unknown) => {
      setConfirmOpen(false);
      toast.error('Could not start the rebuild', { description: describe(err) });
    },
  };

  // Two different intents, one interruption. Compacting reclaims space and
  // leaves the mode alone; the toggle changes the mode and reclaims space as a
  // side effect. They are separate calls because they are separate decisions.
  const onConfirm = () => {
    if (toggleTo) {
      setMode.mutate({ mode: toggleTo }, started);
    } else {
      compact.mutate(undefined, started);
    }
  };

  const onReclaim = () => {
    reclaim.mutate(
      {},
      {
        onSuccess: (res) => {
          if (res.bytes_freed > 0) {
            toast.success(`Reclaimed ${formatBytes(res.bytes_freed)}`);
          } else {
            toast.info('Nothing to reclaim', {
              description: 'The database has no free pages to return right now.',
            });
          }
        },
        onError: (err) => toast.error('Reclaim failed', { description: describe(err) }),
      },
    );
  };

  return (
    <Card>
      <CardHead title="Database" />
      <CardBody className="flex flex-col gap-5">
        {state.isLoading ? <Skeleton className="h-32" /> : null}

        {/* A failure here is usually not a failure. Rebuilding the database
            restarts the server, so for about a minute every request from this
            card is refused — and rendering "TypeError: Failed to fetch" for
            the expected middle of an operation the admin just started reads as
            a broken server. The numbers stay on screen underneath, stale but
            true, rather than being replaced by an error. */}
        {state.isError ? (
          <Callout variant="warn">
            {state.error instanceof ApiError
              ? `Could not read the database state: ${describe(state.error)}`
              : 'The server is not answering. If it was just asked to rebuild the database it is ' +
                'restarting to adopt the new file — this page recovers on its own.'}
          </Callout>
        ) : null}

        {db ? (
          <>
            <StatStrip
              items={[
                { label: 'File size', value: formatBytes(db.file_bytes, { zero: '0 B' }) },
                {
                  label: 'Wasted',
                  value: `${formatBytes(db.reclaimable_bytes, { zero: '0 B' })} (${Math.round(db.reclaimable_percent)}%)`,
                  title: `${db.freelist_pages} free pages of ${db.page_count}`,
                },
                { label: 'Write-ahead log', value: formatBytes(db.wal_bytes, { zero: '0 B' }) },
                { label: 'Reclaim mode', value: db.auto_vacuum },
              ]}
            />

            <Callout variant={VERDICT_VARIANT[db.verdict] ?? 'default'}>{db.verdict_reason}</Callout>

            {running ? (
              <Callout variant="default">
                {op?.message ?? 'A maintenance operation is running.'}
                {op?.bytes_total ? (
                  <>
                    {' '}
                    {formatBytes(op.bytes_done ?? 0)} of {formatBytes(op.bytes_total)}.
                  </>
                ) : null}
              </Callout>
            ) : null}

            <KV
              rows={[
                { label: 'Path', value: <span className="font-mono text-[12px]">{db.path}</span> },
                { label: 'Page size', value: `${db.page_size} B` },
                { label: 'Free disk', value: formatBytes(db.free_disk_bytes, { zero: 'unknown' }) },
                {
                  label: 'A compaction needs',
                  value: formatBytes(db.required_disk_bytes, { zero: '0 B' }),
                  title: 'The rebuilt copy exists alongside the original until the swap.',
                },
              ]}
            />

            <SwitchRow
              id="db-incremental"
              checked={db.auto_vacuum === 'incremental'}
              disabled={busy}
              onCheckedChange={(next) => {
                // Both directions cost a rebuild, and both are offered. The
                // dialog states the price; the decision is the admin's.
                setToggleTo(next ? 'incremental' : 'none');
                setConfirmOpen(true);
              }}
              label="Incremental reclaim"
              hint={
                db.auto_vacuum === 'incremental'
                  ? 'On. Free pages can be returned without a rebuild, at about 1.5% on indexing writes.'
                  : 'Off. Space is only returned by compacting. Switching either way rebuilds the database once.'
              }
            />

            {schedules.data && schedules.data.length > 0 ? (
              <div className="flex flex-col gap-4 border-t pt-4">
                {schedules.data.map((task) => (
                  <ScheduleRow key={task.name} task={task} disabled={busy} />
                ))}
              </div>
            ) : null}
          </>
        ) : null}
      </CardBody>

      {db ? (
        <CardFoot>
          <span className="cix-hint">
            {db.blocked_reason ??
              (db.auto_vacuum === 'incremental'
                ? 'Reclaim is cheap and needs no window. Compacting also defragments.'
                : 'Compacting rebuilds the file and restarts the server.')}
          </span>
          <div className="ml-auto flex items-center gap-2.5">
            {db.auto_vacuum === 'incremental' ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={onReclaim}
                disabled={busy || db.reclaimable_bytes === 0}
              >
                {reclaim.isPending ? <Dots /> : null}
                Reclaim now
              </Button>
            ) : null}
            <Button
              size="sm"
              variant="danger"
              onClick={() => {
                setToggleTo(null);
                setConfirmOpen(true);
              }}
              disabled={busy || !!db.blocked_reason}
              title={db.blocked_reason ?? 'Rebuild the database and reclaim its empty space'}
            >
              {compact.isPending ? <Dots /> : null}
              Compact now
            </Button>
          </div>
        </CardFoot>
      ) : null}

      {db ? (
        <ConfirmCompactDialog
          open={confirmOpen}
          onOpenChange={(next) => (!compact.isPending && !setMode.isPending ? setConfirmOpen(next) : null)}
          onConfirm={onConfirm}
          isPending={compact.isPending || setMode.isPending}
          state={db}
          toggleTo={toggleTo}
        />
      ) : null}
    </Card>
  );
}
