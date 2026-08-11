import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Buttons are square, ink-outlined, and flat. Depth is a hard 4px offset
// shadow and it belongs to exactly one button per screen region — that's the
// `primary` (or `danger`) variant. Everything else is outline-only.
//
//   primary      ink fill, cream text, hard shadow      — the one action
//   danger       accent fill, hard shadow               — destructive, one per region
//   quietDanger  accent outline, fills on hover         — destructive in a row/table
//   default      surface fill, ink outline, no shadow   — everything else
//   ghost        no border, warm hover                  — toolbar / icon affordances
//   link         accent text with a 1.5px underline     — inline in prose
//
// Hover on a shadowed button translates it 2px into its own shadow; the
// remaining variants only change fill. Sizes are 30 / 38 / 46.
const buttonVariants = cva('cix-btn', {
  variants: {
    variant: {
      default: '',
      primary: 'is-primary',
      danger: 'is-danger',
      quietDanger: 'is-quiet-danger',
      ghost: 'is-ghost',
      link: 'cix-link-btn h-auto',
    },
    size: {
      default: '',
      sm: 'is-sm',
      lg: 'is-lg',
      icon: 'is-icon',
      iconSm: 'is-icon is-sm',
    },
  },
  defaultVariants: { variant: 'default', size: 'default' },
});

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, type, ...props }, ref) => {
    const Comp = asChild ? Slot : 'button';
    return (
      <Comp
        ref={ref}
        // Default to type="button": these live inside forms often enough that
        // the HTML default ("submit") has caused accidental submits.
        type={asChild ? undefined : (type ?? 'button')}
        className={cn(
          buttonVariants({ variant, size }),
          '[&_svg]:size-4 [&_svg]:shrink-0',
          className
        )}
        {...props}
      />
    );
  }
);
Button.displayName = 'Button';

// Three squares, the lit one marching left to right. No spinner — nothing in
// this system rotates.
export function Dots({ className }: { className?: string }) {
  return (
    <span className={cn('cix-dots', className)} role="status" aria-label="Loading">
      <i />
      <i />
      <i />
    </span>
  );
}

export { buttonVariants };
