import { cn } from '@/lib/cn';

// Eight outlined cells, filled left to right in accent. Discrete on purpose:
// a continuous bar invites a blur/gradient, and this system has neither.
// `value` is 0..1; anything outside is clamped.
export function Progress({
  value,
  cells = 8,
  label,
  className,
}: {
  value: number;
  cells?: number;
  label?: string;
  className?: string;
}) {
  const pct = Math.min(1, Math.max(0, Number.isFinite(value) ? value : 0));
  const on = Math.round(pct * cells);
  return (
    <div
      className={cn('cix-progress', className)}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct * 100)}
      aria-label={label}
    >
      {Array.from({ length: cells }, (_, i) => (
        <i key={i} className={i < on ? 'is-on' : undefined} />
      ))}
    </div>
  );
}

// Numbered step marker: ink square when done/current, quiet outline when
// pending. Pair with a title and, usually, an attached command block.
export function Step({
  n,
  pending,
  children,
  className,
}: {
  n: number;
  pending?: boolean;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('cix-step', pending && 'is-pending', className)}>
      <span className="cix-step__n" aria-hidden>
        {n}
      </span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
