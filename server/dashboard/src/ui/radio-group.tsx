import * as RadioGroupPrimitive from '@radix-ui/react-radio-group';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

// The radio is the one circle in the system — 18px ring, 9px accent dot.
// Everything else, including the checkbox, is square.
export const RadioGroup = forwardRef<
  ElementRef<typeof RadioGroupPrimitive.Root>,
  ComponentPropsWithoutRef<typeof RadioGroupPrimitive.Root>
>(({ className, ...props }, ref) => (
  <RadioGroupPrimitive.Root ref={ref} className={cn('grid gap-2.5', className)} {...props} />
));
RadioGroup.displayName = RadioGroupPrimitive.Root.displayName;

export const RadioGroupItem = forwardRef<
  ElementRef<typeof RadioGroupPrimitive.Item>,
  ComponentPropsWithoutRef<typeof RadioGroupPrimitive.Item>
>(({ className, ...props }, ref) => (
  <RadioGroupPrimitive.Item
    ref={ref}
    className={cn(
      'flex h-[18px] w-[18px] flex-none items-center justify-center rounded-full border bg-surface',
      'disabled:cursor-default disabled:border-line-quiet disabled:bg-track',
      className
    )}
    {...props}
  >
    <RadioGroupPrimitive.Indicator className="block h-[9px] w-[9px] rounded-full bg-accent" />
  </RadioGroupPrimitive.Item>
));
RadioGroupItem.displayName = RadioGroupPrimitive.Item.displayName;

// A radio rendered as a selectable panel — the shape the Server page uses for
// "Hugging Face / Local file". The whole box is the hit target; the selected
// one gets the ink outline, the rest stay quiet.
export function RadioCard({
  value,
  id,
  title,
  hint,
  selected,
  disabled,
  className,
}: {
  value: string;
  id: string;
  title: ReactNode;
  hint?: ReactNode;
  selected: boolean;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <label
      htmlFor={id}
      className={cn(
        'flex min-w-0 cursor-pointer items-start gap-3 border p-3.5 transition-colors',
        selected ? 'border-ink bg-surface' : 'border-line-quiet bg-surface hover:bg-warm',
        disabled && 'cursor-default opacity-60',
        className
      )}
    >
      <RadioGroupItem value={value} id={id} disabled={disabled} className="mt-0.5" />
      <span className="min-w-0">
        <span className="block text-sm font-semibold">{title}</span>
        {hint ? <span className="cix-hint mt-1 block truncate">{hint}</span> : null}
      </span>
    </label>
  );
}
