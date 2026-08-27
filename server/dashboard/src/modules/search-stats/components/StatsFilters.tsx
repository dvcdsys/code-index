import { Button } from '@/ui/button';
import { Field, Input } from '@/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { cn } from '@/lib/cn';
import { EMPTY_FILTERS, KINDS, WINDOWS, type StatsFilters } from '../hooks';

// A range is two inputs under one label rather than two labelled fields: the
// pair is one filter, and labelling the halves separately reads as two.
function RangeField({
  label,
  hint,
  min,
  max,
  onMin,
  onMax,
}: {
  label: string;
  hint: string;
  min: string;
  max: string;
  onMin: (v: string) => void;
  onMax: (v: string) => void;
}) {
  return (
    <Field label={label} hint={hint}>
      <div className="flex items-center gap-1.5">
        <Input
          type="number"
          min={0}
          inputMode="numeric"
          placeholder="min"
          value={min}
          onChange={(e) => onMin(e.target.value)}
          aria-label={`${label} — minimum`}
          className="w-full"
        />
        <span aria-hidden className="font-mono text-[11px] text-muted">
          –
        </span>
        <Input
          type="number"
          min={0}
          inputMode="numeric"
          placeholder="max"
          value={max}
          onChange={(e) => onMax(e.target.value)}
          aria-label={`${label} — maximum`}
          className="w-full"
        />
      </div>
    </Field>
  );
}

export function StatsFiltersBar({
  filters,
  onChange,
}: {
  filters: StatsFilters;
  onChange: (next: StatsFilters) => void;
}) {
  // Every filter change resets to the first page. Staying on page 4 of a
  // result set that just shrank to one page shows an empty table and reads as
  // "no matches" when there are plenty.
  const set = (patch: Partial<StatsFilters>) => onChange({ ...filters, ...patch, page: 0 });

  const toggleKind = (kind: string) => {
    const next = filters.kinds.includes(kind)
      ? filters.kinds.filter((k) => k !== kind)
      : [...filters.kinds, kind];
    set({ kinds: next });
  };

  const dirty =
    filters.kinds.length > 0 ||
    filters.project !== '' ||
    filters.minQueries !== '' ||
    filters.maxQueries !== '' ||
    filters.minTopFileHits !== '' ||
    filters.maxTopFileHits !== '' ||
    filters.window !== EMPTY_FILTERS.window;

  return (
    <div className="cix-card mb-4 flex flex-col gap-4 p-[18px]">
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <Field label="Window" hint="Buckets are kept for 7 days; older counts survive only in the totals.">
          <Select value={filters.window} onValueChange={(v) => set({ window: v as StatsFilters['window'] })}>
            <SelectTrigger aria-label="Time window">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {WINDOWS.map((w) => (
                <SelectItem key={w.value} value={w.value}>
                  {w.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field label="Project" hint="Matches the name or the path.">
          <Input
            value={filters.project}
            onChange={(e) => set({ project: e.target.value })}
            placeholder="filter by name or path"
            aria-label="Filter by project"
          />
        </Field>

        <RangeField
          label="Queries per project"
          hint="Searches counted in the selected window."
          min={filters.minQueries}
          max={filters.maxQueries}
          onMin={(v) => set({ minQueries: v })}
          onMax={(v) => set({ maxQueries: v })}
        />

        <RangeField
          label="Hits on the top file"
          hint="How many searches the project's most-returned file appeared in."
          min={filters.minTopFileHits}
          max={filters.maxTopFileHits}
          onMin={(v) => set({ minTopFileHits: v })}
          onMax={(v) => set({ maxTopFileHits: v })}
        />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="cix-label">Kinds</span>
        {KINDS.map((k) => {
          const active = filters.kinds.includes(k.value);
          return (
            <button
              key={k.value}
              type="button"
              aria-pressed={active}
              onClick={() => toggleKind(k.value)}
              className={cn(
                'border px-2 py-[3px] font-mono text-[11px] transition-colors',
                active
                  ? 'border-ink bg-ink text-surface'
                  : 'border-line-quiet text-muted hover:border-line hover:text-ink'
              )}
            >
              {k.label}
            </button>
          );
        })}
        {/* No selection means every kind — said out loud, because an empty
            row of unselected chips otherwise reads as "nothing is included". */}
        {filters.kinds.length === 0 ? (
          <span className="font-mono text-[11px] text-muted">all kinds</span>
        ) : null}

        {dirty ? (
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto"
            onClick={() => onChange({ ...EMPTY_FILTERS, sort: filters.sort, desc: filters.desc })}
          >
            Clear filters
          </Button>
        ) : null}
      </div>
    </div>
  );
}
