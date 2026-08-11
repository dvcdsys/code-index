import { forwardRef, type HTMLAttributes, type TdHTMLAttributes, type ThHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Real <table> semantics inside a rounded card. The header strip is filled
// and mono-capped; body rows are divided by soft 1.5px rules; numeric and
// action columns align right because the right edge is the reading axis.
//
// Wrap in <Card> (or pass `card`) so the 12px radius clips the header strip.

export const Table = forwardRef<
  HTMLTableElement,
  HTMLAttributes<HTMLTableElement> & { card?: boolean }
>(({ className, card, ...props }, ref) => {
  const table = <table ref={ref} className={cn('cix-table', className)} {...props} />;
  if (!card) return <div className="w-full overflow-x-auto">{table}</div>;
  return (
    <div className="cix-card">
      <div className="w-full overflow-x-auto">{table}</div>
    </div>
  );
});
Table.displayName = 'Table';

export const THead = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => <thead ref={ref} className={className} {...props} />
);
THead.displayName = 'THead';

export const TBody = forwardRef<HTMLTableSectionElement, HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => <tbody ref={ref} className={className} {...props} />
);
TBody.displayName = 'TBody';

export const TR = forwardRef<HTMLTableRowElement, HTMLAttributes<HTMLTableRowElement>>(
  ({ className, ...props }, ref) => <tr ref={ref} className={className} {...props} />
);
TR.displayName = 'TR';

export const TH = forwardRef<
  HTMLTableCellElement,
  ThHTMLAttributes<HTMLTableCellElement> & { align?: 'left' | 'right' }
>(({ className, align, ...props }, ref) => (
  <th ref={ref} className={cn(align === 'right' && 'text-right', className)} {...props} />
));
TH.displayName = 'TH';

export const TD = forwardRef<
  HTMLTableCellElement,
  TdHTMLAttributes<HTMLTableCellElement> & { mono?: boolean; align?: 'left' | 'right' }
>(({ className, mono, align, ...props }, ref) => (
  <td
    ref={ref}
    className={cn(mono && 'cix-mono', align === 'right' && 'text-right', className)}
    {...props}
  />
));
TD.displayName = 'TD';

// A line under a table carrying its count and any one-line caveat. Mono,
// muted, outside the card — never a modal-only surprise.
export function TableNote({
  left,
  right,
  className,
}: {
  left?: React.ReactNode;
  right?: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        'flex flex-wrap items-center justify-between gap-3 pt-2.5 font-mono text-[11.5px] text-muted',
        className
      )}
    >
      <span>{left}</span>
      <span>{right}</span>
    </div>
  );
}
