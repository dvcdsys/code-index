import * as LabelPrimitive from '@radix-ui/react-label';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react';
import { cn } from '@/lib/cn';

// The system has exactly one label style: mono 11, uppercase, .16em tracking,
// muted. Anything that wants sentence-case prose is a paragraph, not a label.
export const Label = forwardRef<
  ElementRef<typeof LabelPrimitive.Root>,
  ComponentPropsWithoutRef<typeof LabelPrimitive.Root>
>(({ className, ...props }, ref) => (
  <LabelPrimitive.Root ref={ref} className={cn('cix-label', className)} {...props} />
));
Label.displayName = LabelPrimitive.Root.displayName;
