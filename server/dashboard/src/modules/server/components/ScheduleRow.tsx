import { useEffect, useState } from 'react';
import { ApiError } from '@/api/client';
import type { ScheduledTask } from '@/api/types';
import { formatDateTime } from '@/lib/formatDate';
import { toast } from 'sonner';
import { Button, Dots } from '@/ui/button';
import { SwitchRow } from '@/ui/switch';
import { useUpdateSchedule } from '../hooks';
import { SourcePill } from './SourcePill';

// The shapes people actually want, so the common case is a click.
//
// The field stays visible and editable next to them: crontab is the contract,
// and a preset that quietly rewrote itself into something the field could not
// express would be worse than no presets at all.
const PRESETS: Array<{ label: string; cron: string }> = [
  { label: 'Midnight', cron: '0 0 * * *' },
  { label: '03:00 daily', cron: '0 3 * * *' },
  { label: 'Every 6 hours', cron: '0 */6 * * *' },
  { label: 'Sunday 04:00', cron: '0 4 * * 0' },
];

interface Props {
  task: ScheduledTask;
  disabled?: boolean;
}

export function ScheduleRow({ task, disabled }: Props) {
  const update = useUpdateSchedule();
  const [cron, setCron] = useState(task.cron);

  // Follow the server when it changes underneath — a save returns the task as
  // it now resolves, and the field must not keep showing what was typed if the
  // server settled on something else.
  useEffect(() => setCron(task.cron), [task.cron]);

  const dirty = cron.trim() !== task.cron;
  const busy = disabled || update.isPending;

  const save = (patch: { cron?: string; enabled?: boolean }) => {
    update.mutate(
      { name: task.name, ...patch },
      {
        onError: (err) => {
          setCron(task.cron);
          toast.error('Could not save the schedule', {
            description: err instanceof ApiError ? err.detail : String(err),
          });
        },
      }
    );
  };

  return (
    <div className="flex flex-col gap-3">
      <SwitchRow
        id={`sched-${task.name}`}
        checked={task.enabled}
        disabled={busy}
        onCheckedChange={(next) => save({ enabled: next })}
        label={task.title}
        hint={task.description ?? undefined}
      />

      {task.enabled ? (
        <div className="flex flex-col gap-2 pl-1">
          <div className="flex flex-wrap items-center gap-2">
            <label className="cix-hint w-[92px] shrink-0" htmlFor={`cron-${task.name}`}>
              Runs at
            </label>
            <input
              id={`cron-${task.name}`}
              className="cix-input w-[150px] font-mono text-[12px]"
              value={cron}
              spellCheck={false}
              disabled={busy}
              onChange={(e) => setCron(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && dirty) save({ cron: cron.trim() });
                if (e.key === 'Escape') setCron(task.cron);
              }}
              aria-label={`${task.title} schedule, crontab expression`}
            />
            {!task.configured ? <SourcePill source="recommended" /> : null}
            {PRESETS.map((p) => (
              <Button
                key={p.cron}
                size="sm"
                variant="ghost"
                disabled={busy || p.cron === cron.trim()}
                onClick={() => {
                  setCron(p.cron);
                  save({ cron: p.cron });
                }}
              >
                {p.label}
              </Button>
            ))}
            {dirty ? (
              <Button size="sm" disabled={busy} onClick={() => save({ cron: cron.trim() })}>
                {update.isPending ? <Dots /> : null}
                Save
              </Button>
            ) : null}
          </div>

          <NextRuns task={task} />
        </div>
      ) : null}
    </div>
  );
}

// Milliseconds are the wire unit; nobody reads a schedule in milliseconds.
function formatDuration(ms: number): string {
  if (ms < 1_000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)} s`;
  return `${Math.round(ms / 60_000)} min`;
}

// The preview comes from the server, computed by the parser that will actually
// fire the task. Evaluating the expression here would be a second cron
// implementation whose only job is to disagree with the first one.
function NextRuns({ task }: { task: ScheduledTask }) {
  const runs = task.next_runs ?? [];
  return (
    <span className="cix-hint pl-[100px]">
      {task.running ? (
        <>
          <span className="cix-dot is-busy mr-1.5" aria-hidden />
          Running now ·{' '}
        </>
      ) : null}
      {runs.length > 0 ? (
        <>
          Next: {runs.map((r) => formatDateTime(r)).join(' · ')}
        </>
      ) : (
        'This expression has no upcoming run.'
      )}
      {task.last_run_at ? (
        <>
          {' · '}last ran {formatDateTime(task.last_run_at)}
          {task.last_millis ? ` in ${formatDuration(task.last_millis)}` : null}
          {task.last_status === 'failed' ? ` — failed: ${task.last_error ?? 'see the log'}` : null}
          {task.last_status === 'interrupted' ? ' — interrupted' : null}
        </>
      ) : null}
      {task.updated_by ? <>{' · '}set by {task.updated_by}</> : null}
      {!task.catch_up ? ' · a run missed while the server is down is skipped, not run late.' : null}
    </span>
  );
}
