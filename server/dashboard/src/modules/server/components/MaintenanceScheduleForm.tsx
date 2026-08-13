import { useEffect, useState } from 'react';
import { ApiError } from '@/api/client';
import type { DatabaseState, MaintenanceScheduleUpdate } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';
import { formatDateTime } from '@/lib/formatDate';
import { toast } from 'sonner';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Skeleton } from '@/ui/skeleton';
import { SwitchRow } from '@/ui/switch';
import { useMaintenanceSchedule, useUpdateMaintenanceSchedule } from '../hooks';
import { SourcePill } from './SourcePill';

type Mode = DatabaseState['auto_vacuum'];

interface Props {
  disabled?: boolean;
  mode: Mode;
}

// The shared formatter renders an absent date as an em dash. Here the absence
// is the interesting part — an upgraded server has no history at all — so it
// gets words instead.
function formatWhen(iso: string | null | undefined): string {
  return iso ? formatDateTime(iso) : 'never run';
}

export function MaintenanceScheduleForm({ disabled, mode }: Props) {
  const schedule = useMaintenanceSchedule();
  const update = useUpdateMaintenanceSchedule();
  const s = schedule.data;

  const [interval, setInterval] = useState('24');
  const [windowed, setWindowed] = useState(false);
  const [start, setStart] = useState('2');
  const [end, setEnd] = useState('6');

  useEffect(() => {
    if (!s) return;
    setInterval(String(s.interval_hours));
    setWindowed(s.window_start_hour !== null && s.window_start_hour !== undefined);
    if (s.window_start_hour !== null && s.window_start_hour !== undefined) {
      setStart(String(s.window_start_hour));
    }
    if (s.window_end_hour !== null && s.window_end_hour !== undefined) {
      setEnd(String(s.window_end_hour));
    }
  }, [s]);

  if (schedule.isLoading) return <Skeleton className="h-24" />;
  if (schedule.isError || !s) {
    return (
      <Callout variant="warn">
        Could not read the maintenance schedule:{' '}
        {schedule.error instanceof ApiError ? schedule.error.detail : String(schedule.error)}
      </Callout>
    );
  }

  const save = (patch: MaintenanceScheduleUpdate) => {
    update.mutate(patch, {
      onError: (err) =>
        toast.error('Could not save the schedule', {
          description: err instanceof ApiError ? err.detail : String(err),
        }),
    });
  };

  const dirty =
    interval !== String(s.interval_hours) ||
    windowed !== (s.window_start_hour !== null && s.window_start_hour !== undefined) ||
    (windowed && (start !== String(s.window_start_hour) || end !== String(s.window_end_hour)));

  return (
    <div className="flex flex-col gap-3 border-t pt-4">
      <div className="flex items-baseline gap-2">
        <span className="text-[13px] font-semibold">Automatic reclaim</span>
        <SourcePill source={s.source.enabled} />
        <span className="cix-hint ml-auto">
          Last run: {formatWhen(s.last_run_at)}
          {s.last_status === 'ok' && s.last_freed_bytes
            ? ` — freed ${formatBytes(s.last_freed_bytes)}`
            : null}
          {s.last_status === 'failed' ? ' — failed' : null}
        </span>
      </div>

      {s.last_error ? <Callout variant="warn">{s.last_error}</Callout> : null}

      <SwitchRow
        id="db-schedule-enabled"
        checked={s.enabled}
        disabled={disabled || update.isPending}
        onCheckedChange={(next) => save({ enabled: next })}
        label={s.mode === 'full' ? 'Compact automatically' : 'Reclaim free pages automatically'}
        hint={
          s.mode === 'full'
            ? 'Rebuilds the database on a schedule. Takes the server read-only and restarts it — set a window below.'
            : mode === 'incremental'
              ? 'Returns free pages to the filesystem on a schedule. No window, no restart.'
              : 'Needs incremental reclaim, which is off for this database.'
        }
      />

      {!s.configured ? (
        <span className="cix-hint">
          Not configured yet — these are the defaults for a database in{' '}
          <b>{mode === 'incremental' ? 'incremental' : 'no-reclaim'}</b> mode.
        </span>
      ) : null}

      <div className="flex flex-wrap items-end gap-4">
        <label className="flex flex-col gap-1 text-[12.5px]">
          <span className="flex items-center gap-1.5">
            Every (hours) <SourcePill source={s.source.interval_hours} />
          </span>
          <input
            type="number"
            min={1}
            value={interval}
            disabled={disabled || update.isPending}
            onChange={(e) => setInterval(e.target.value)}
            className="w-24 border px-2 py-1 font-mono"
          />
        </label>

        <label className="flex items-center gap-2 text-[12.5px]">
          <input
            type="checkbox"
            checked={windowed}
            disabled={disabled || update.isPending}
            onChange={(e) => setWindowed(e.target.checked)}
          />
          Only between
        </label>
        {windowed ? (
          <div className="flex items-end gap-2 text-[12.5px]">
            <input
              type="number"
              min={0}
              max={23}
              value={start}
              disabled={disabled || update.isPending}
              onChange={(e) => setStart(e.target.value)}
              className="w-16 border px-2 py-1 font-mono"
            />
            <span className="pb-1.5">and</span>
            <input
              type="number"
              min={0}
              max={23}
              value={end}
              disabled={disabled || update.isPending}
              onChange={(e) => setEnd(e.target.value)}
              className="w-16 border px-2 py-1 font-mono"
            />
            <span className="pb-1.5 text-muted">o&rsquo;clock</span>
          </div>
        ) : null}

        <Button
          size="sm"
          className="ml-auto"
          disabled={!dirty || disabled || update.isPending}
          onClick={() =>
            save({
              interval_hours: Number(interval),
              // Explicit null clears the window; leaving the field out would
              // mean "don't change it", which is a different thing.
              window_start_hour: windowed ? Number(start) : null,
              window_end_hour: windowed ? Number(end) : null,
            })
          }
        >
          {update.isPending ? <Dots /> : null}
          Save schedule
        </Button>
      </div>

      <span className="cix-hint">
        A run starts only when everything lines up: enabled, the interval has passed, the waste is
        over both thresholds ({s.min_free_percent}% and{' '}
        {formatBytes(s.min_free_bytes, { zero: '0 B' })}), the clock is inside the window, and no
        indexing is in flight.
      </span>
    </div>
  );
}
