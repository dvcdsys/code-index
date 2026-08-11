import { cva, type VariantProps } from 'class-variance-authority';
import { forwardRef, type HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Callout: 1.5px outline plus a 6px bar on the left edge. Replaces every
// "long explanatory paragraph floating in the layout" — prose that matters
// gets a border, prose that doesn't gets deleted.
const calloutVariants = cva('cix-callout', {
  variants: {
    variant: {
      default: '',
      warn: 'is-warn',
      danger: 'is-danger',
      ok: 'is-ok',
    },
  },
  defaultVariants: { variant: 'default' },
});

export const Callout = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & VariantProps<typeof calloutVariants>
>(({ className, variant, ...props }, ref) => (
  <div
    ref={ref}
    role={variant === 'danger' ? 'alert' : 'note'}
    className={cn(calloutVariants({ variant }), className)}
    {...props}
  />
));
Callout.displayName = 'Callout';

export const CalloutTitle = forwardRef<HTMLElement, HTMLAttributes<HTMLElement>>(
  ({ className, ...props }, ref) => <b ref={ref} className={className} {...props} />
);
CalloutTitle.displayName = 'CalloutTitle';

export const CalloutBody = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(
  ({ className, ...props }, ref) => <p ref={ref} className={className} {...props} />
);
CalloutBody.displayName = 'CalloutBody';
