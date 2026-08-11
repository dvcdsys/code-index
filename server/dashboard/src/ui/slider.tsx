import * as SliderPrimitive from '@radix-ui/react-slider';
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react';
import { cn } from '@/lib/cn';

// 8px outlined track, accent fill, a 14×18 ink block for a thumb. The value
// is rendered by the caller, mono and right-aligned above the track.
export const Slider = forwardRef<
  ElementRef<typeof SliderPrimitive.Root>,
  ComponentPropsWithoutRef<typeof SliderPrimitive.Root>
>(({ className, ...props }, ref) => (
  <SliderPrimitive.Root
    ref={ref}
    className={cn('relative flex h-[18px] w-full touch-none select-none items-center', className)}
    {...props}
  >
    <SliderPrimitive.Track className="relative h-2 w-full grow border bg-track">
      <SliderPrimitive.Range className="absolute h-full bg-accent" />
    </SliderPrimitive.Track>
    <SliderPrimitive.Thumb
      className="block h-[18px] w-3.5 bg-ink disabled:bg-line-quiet"
      aria-label="Value"
    />
  </SliderPrimitive.Root>
));
Slider.displayName = SliderPrimitive.Root.displayName;

// Label on the left, mono value on the right, track underneath — the layout
// every numeric range in the dashboard uses.
export function SliderField({
  label,
  value,
  display,
  onChange,
  min,
  max,
  step,
  className,
}: {
  label: string;
  value: number;
  display?: string;
  onChange: (v: number) => void;
  min: number;
  max: number;
  step?: number;
  className?: string;
}) {
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div className="flex items-baseline justify-between gap-3">
        <span className="cix-label">{label}</span>
        <span className="font-mono text-[13px] tabular-nums">{display ?? value}</span>
      </div>
      <Slider
        value={[value]}
        min={min}
        max={max}
        step={step}
        onValueChange={(v) => onChange(v[0] ?? value)}
      />
    </div>
  );
}
