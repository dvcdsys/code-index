import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// 12px bars in the track colour. No shimmer, no pulse — the system has no
// animated gradients, and a still bar reads as "not loaded yet" perfectly.
export function Skeleton({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('cix-skel', className)} {...props} />;
}

// A stack of bars for list/table placeholders, last one short so the block
// reads as text rather than as a filled box.
export function SkeletonRows({ rows = 3, className }: { rows?: number; className?: string }) {
  return (
    <div className={cn('flex flex-col gap-2.5', className)} aria-hidden>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className={i === rows - 1 ? 'w-1/3' : 'w-full'} />
      ))}
    </div>
  );
}
