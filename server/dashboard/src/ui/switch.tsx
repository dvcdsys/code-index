import * as SwitchPrimitives from '@radix-ui/react-switch';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react';
import { cn } from '@/lib/cn';

// 34×18 square track, 14×12 knob, green when on. Square, like every other
// control — the pill toggle is the one shape this system refuses.
export const Switch = forwardRef<
  ElementRef<typeof SwitchPrimitives.Root>,
  ComponentPropsWithoutRef<typeof SwitchPrimitives.Root>
>(({ className, ...props }, ref) => (
  <SwitchPrimitives.Root
    ref={ref}
    className={cn(
      'relative inline-flex h-[18px] w-[34px] flex-none cursor-pointer items-center border bg-track',
      'transition-colors data-[state=checked]:bg-ok',
      'disabled:cursor-default disabled:border-line-quiet disabled:bg-track',
      className
    )}
    {...props}
  >
    <SwitchPrimitives.Thumb
      className={cn(
        'pointer-events-none block h-3 w-3.5 bg-surface transition-transform',
        'translate-x-[1px] data-[state=checked]:translate-x-[17px]'
      )}
    />
  </SwitchPrimitives.Root>
));
Switch.displayName = SwitchPrimitives.Root.displayName;

// Toggle + word, the pairing the system always uses: a colour alone never
// carries state.
export function SwitchRow({
  checked,
  onCheckedChange,
  label,
  hint,
  disabled,
  id,
  className,
}: {
  checked: boolean;
  onCheckedChange: (v: boolean) => void;
  label: string;
  hint?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
}) {
  return (
    <div className={cn('flex items-start gap-2.5', className)}>
      <Switch
        id={id}
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
        className="mt-0.5"
      />
      <label htmlFor={id} className="min-w-0 cursor-pointer select-none">
        <span className={cn('block text-sm', disabled && 'text-faint')}>{label}</span>
        {hint ? <span className="cix-hint mt-0.5 block">{hint}</span> : null}
      </label>
    </div>
  );
}
