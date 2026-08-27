import { ApiError } from '@/api/client';
import type { SearchStatsSettings } from '@/api/types';
import { Callout } from '@/ui/alert';
import { Switch } from '@/ui/switch';
import { formatRelative } from '@/lib/formatDate';

// What decided the current state, said in a sentence rather than as a badge
// nobody can decode. The distinction matters when the answer is "off": a
// deployment that never asked for counters reads differently from one where an
// admin turned them off on purpose.
function provenance(s: SearchStatsSettings): string {
  switch (s.source) {
    case 'database':
      return s.updated_by
        ? `Set by ${s.updated_by} ${formatRelative(s.updated_at)}.`
        : `Set from this dashboard ${formatRelative(s.updated_at)}.`;
    case 'environment':
      return 'Set at deployment with CIX_SEARCH_STATS_ENABLED. Changing it here overrides that from now on.';
    default:
      return 'Nobody has turned this on. Counters are off by default.';
  }
}

export function CollectionToggle({
  settings,
  canEdit,
  pending,
  error,
  onChange,
}: {
  settings: SearchStatsSettings;
  canEdit: boolean;
  pending: boolean;
  error: unknown;
  onChange: (enabled: boolean) => void;
}) {
  return (
    <div className="cix-card mb-4 flex flex-col gap-3 p-[18px]">
      <div className="flex items-start gap-4">
        <span className="min-w-0 flex-1">
          <span className="cix-label">Collect search statistics</span>
          <p className="m-0 pt-1 text-sm text-dim">
            {settings.enabled
              ? 'Every search records which project it ran against and which files came back. Counters only — no query text.'
              : 'Nothing is being recorded. Turning this on starts counting from now; it does not backfill.'}{' '}
            {provenance(settings)}
          </p>
        </span>
        <span className="flex flex-none items-center gap-2.5 pt-0.5">
          <Switch
            checked={settings.enabled}
            disabled={!canEdit || pending}
            onCheckedChange={onChange}
            aria-label="Collect search statistics"
          />
          <span className="w-8 font-mono text-[11px] text-muted">
            {pending ? '…' : settings.enabled ? 'on' : 'off'}
          </span>
        </span>
      </div>

      {/* Everyone can see the state; only an admin can change it. Saying so is
          better than a control that silently does nothing. */}
      {!canEdit ? (
        <p className="m-0 font-mono text-[11px] text-muted">
          Only an admin can change this.
        </p>
      ) : null}

      {error ? (
        <Callout variant="danger">
          <b>Could not change the setting</b>
          <p>{error instanceof ApiError ? error.detail : String(error)}</p>
        </Callout>
      ) : null}
    </div>
  );
}
