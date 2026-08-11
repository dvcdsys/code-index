import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

// The card is the only rounded box in the system (12px) and the only place a
// blur-free border radius appears at all. Its header is a filled strip with a
// 1.5px rule under it — that strip is where a card's ENV / provider badge and
// its one inline action live.

export const Card = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { clickable?: boolean }
>(({ className, clickable, ...props }, ref) => (
  <div ref={ref} className={cn('cix-card', clickable && 'is-clickable', className)} {...props} />
));
Card.displayName = 'Card';

export const CardHead = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { title?: ReactNode; aside?: ReactNode }
>(({ className, title, aside, children, ...props }, ref) => (
  <div ref={ref} className={cn('cix-card__head', className)} {...props}>
    {title !== undefined ? <span className="min-w-0 truncate">{title}</span> : null}
    {children}
    {aside ? <div className="ml-auto flex items-center gap-2.5">{aside}</div> : null}
  </div>
));
CardHead.displayName = 'CardHead';

export const CardBody = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn('cix-card__body', className)} {...props} />
  )
);
CardBody.displayName = 'CardBody';

export const CardFoot = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn('cix-card__foot', className)} {...props} />
  )
);
CardFoot.displayName = 'CardFoot';

// Key/value block — the correct home for read-only facts. Values are mono and
// right-aligned; the right edge is the reading axis. Never render facts as
// disabled inputs or greyed menu items.
export function KV({
  rows,
  className,
}: {
  rows: Array<{ label: string; value: ReactNode; title?: string }>;
  className?: string;
}) {
  return (
    <dl className={cn('cix-kv', className)}>
      {rows.map((r) => (
        <div key={r.label} className="contents">
          <dt>{r.label}</dt>
          <dd title={r.title}>{r.value}</dd>
        </div>
      ))}
    </dl>
  );
}

// A strip of facts divided by 1.5px rules — no gaps, no floating boxes.
export function StatStrip({
  items,
  className,
}: {
  items: Array<{ label: string; value: ReactNode; title?: string }>;
  className?: string;
}) {
  return (
    <div
      className={cn('cix-stats', className)}
      style={{ gridTemplateColumns: `repeat(${items.length}, minmax(0, 1fr))` }}
    >
      {items.map((s) => (
        <div key={s.label} className="min-w-0">
          <span className="cix-label">{s.label}</span>
          <b title={s.title}>{s.value}</b>
        </div>
      ))}
    </div>
  );
}

// Empty state — dashed outline, three outline squares, a title and exactly
// one sentence. Optional single action.
export function Empty({
  title,
  children,
  action,
  className,
}: {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('cix-empty', className)}>
      <span className="flex gap-1.5" aria-hidden>
        <i className="block h-2.5 w-2.5 border border-line-quiet" />
        <i className="block h-2.5 w-2.5 border border-line-quiet" />
        <i className="block h-2.5 w-2.5 border border-line-quiet" />
      </span>
      <b className="text-[15px] font-bold">{title}</b>
      {children ? <p className="m-0 max-w-md text-sm text-dim">{children}</p> : null}
      {action}
    </div>
  );
}
