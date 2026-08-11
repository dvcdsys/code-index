import {
  forwardRef,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react';
import { cn } from '@/lib/cn';

// h38, square, 1.5px ink, mono 13. Machine values are typed into these, so
// the mono face is the default rather than an opt-in.
export const Input = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }
>(({ className, invalid, ...props }, ref) => (
  <input
    ref={ref}
    aria-invalid={invalid || undefined}
    className={cn('cix-input', invalid && 'is-error', className)}
    {...props}
  />
));
Input.displayName = 'Input';

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }
>(({ className, invalid, rows = 4, ...props }, ref) => (
  <textarea
    ref={ref}
    rows={rows}
    aria-invalid={invalid || undefined}
    className={cn('cix-input', invalid && 'is-error', className)}
    {...props}
  />
));
Textarea.displayName = 'Textarea';

// Field wraps label + control + hint/error in the one vertical rhythm the
// whole dashboard uses. The error message replaces the hint rather than
// stacking under it — two lines of mono under a 38px field is noise.
export function Field({
  label,
  hint,
  error,
  htmlFor,
  className,
  children,
}: {
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  htmlFor?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn('cix-field', className)}>
      {label ? (
        <label className="cix-label" htmlFor={htmlFor}>
          {label}
        </label>
      ) : null}
      {children}
      {error ? (
        <span className="cix-error">{error}</span>
      ) : hint ? (
        <span className="cix-hint">{hint}</span>
      ) : null}
    </div>
  );
}
