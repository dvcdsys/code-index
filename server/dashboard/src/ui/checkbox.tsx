import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

// 18×18 square; checked is an ink fill with an 8px cream square punched out
// of the middle. Built on a real <input type="checkbox"> so keyboard, form
// submission and screen readers all work without extra wiring.
export const Checkbox = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>
>(({ className, ...props }, ref) => (
  <input
    ref={ref}
    type="checkbox"
    className={cn(
      'relative h-[18px] w-[18px] flex-none cursor-pointer appearance-none border bg-surface',
      'checked:bg-ink',
      "checked:after:absolute checked:after:left-1/2 checked:after:top-1/2 checked:after:h-2 checked:after:w-2 checked:after:-translate-x-1/2 checked:after:-translate-y-1/2 checked:after:bg-surface checked:after:content-['']",
      'disabled:cursor-default disabled:border-line-quiet disabled:bg-track',
      className
    )}
    {...props}
  />
));
Checkbox.displayName = 'Checkbox';

export function CheckboxRow({
  id,
  checked,
  onCheckedChange,
  label,
  hint,
  disabled,
  className,
}: {
  id: string;
  checked: boolean;
  onCheckedChange: (v: boolean) => void;
  label: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <div className={cn('flex items-start gap-2.5', className)}>
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onCheckedChange(e.currentTarget.checked)}
        className="mt-0.5"
      />
      <label htmlFor={id} className="min-w-0 cursor-pointer select-none">
        <span className={cn('block text-sm', disabled && 'text-faint')}>{label}</span>
        {hint ? <span className="cix-hint mt-0.5 block">{hint}</span> : null}
      </label>
    </div>
  );
}
