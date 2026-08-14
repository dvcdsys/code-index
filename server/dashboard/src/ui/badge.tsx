import { cva, type VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Square mono chips. Solid fills carry *state* (ok / busy / warn); outlined
// chips carry *meta* (ENV, admin, a provider name). Never decorative.
const badgeVariants = cva('cix-badge', {
  variants: {
    variant: {
      default: '',
      outline: 'is-outline',
      quiet: 'is-quiet',
      ok: 'is-ok',
      busy: 'is-busy',
      warn: 'is-warn',
      ink: 'is-ink',
    },
  },
  defaultVariants: { variant: 'default' },
});

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

// A 9×9 square plus a word. Colour never carries the meaning alone — the
// label is mandatory, which is why it is a required child and not a prop.
export function StatusDot({
  tone = 'idle',
  className,
}: {
  tone?: 'ok' | 'busy' | 'warn' | 'idle';
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        'cix-dot',
        tone === 'ok' && 'is-ok',
        tone === 'busy' && 'is-busy',
        tone === 'warn' && 'is-warn',
        className
      )}
    />
  );
}

export function Status({
  tone = 'idle',
  children,
  className,
}: {
  tone?: 'ok' | 'busy' | 'warn' | 'idle';
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span className={cn('inline-flex items-center gap-2', className)}>
      <StatusDot tone={tone} />
      <span>{children}</span>
    </span>
  );
}

export { badgeVariants };
