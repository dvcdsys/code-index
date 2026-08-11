import { useId, type ReactNode } from 'react';
import { Input } from '@/ui/input';
import { SourcePill } from './SourcePill';

// The shape every runtime-config number uses: mono-caps label with the raw
// config key beside it, the source pill on the right, a narrow numeric input,
// and one line of hint. Sharing it keeps four sections from drifting apart.
//
// Three emphasis levels, deliberately — the value you act on, the label that
// names it, and everything else. A field previously spent six different tones
// on those three jobs (label muted, key faint, pill white-on-ink, value ink,
// hint dim, "Recommended N" ink again), and a two-column grid of them read as
// speckle. The key now shares the label's tone and the recommendation shares
// the hint's; each stays legible on its own by case and by face, not by yet
// another colour.
export function NumberField({
  field,
  label,
  hint,
  value,
  recommended,
  source,
  onChange,
  min = 0,
}: {
  field: string;
  label: string;
  hint: ReactNode;
  value: number;
  recommended?: number | string;
  source?: string;
  onChange: (next: number) => void;
  min?: number;
}) {
  const id = useId();
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={id} className="cix-label">
          {label} <span className="normal-case tracking-normal">({field})</span>
        </label>
        <SourcePill source={source} />
      </div>
      <Input
        id={id}
        type="number"
        min={min}
        className="max-w-[200px]"
        value={Number.isFinite(value) ? value : 0}
        onChange={(e) => {
          const n = parseInt(e.target.value, 10);
          onChange(Number.isFinite(n) ? n : 0);
        }}
      />
      <p className="m-0 text-[13px] leading-snug text-dim">
        {hint}
        {recommended !== undefined ? (
          <>
            {' '}
            Recommended <span className="font-mono">{recommended}</span>.
          </>
        ) : null}
      </p>
    </div>
  );
}
