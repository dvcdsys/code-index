import { useCopy } from '@/lib/useCopy';
import { cn } from '@/lib/cn';

// Dark well with the Copy button attached *inside* the same 1.5px outline —
// not floating beside it. `command` is copied verbatim, so what is rendered
// is exactly what gets pasted.
export function CodeBlock({
  command,
  className,
  wrap,
}: {
  command: string;
  className?: string;
  /** Multi-line snippets wrap; one-liners truncate so the row stays h≈40. */
  wrap?: boolean;
}) {
  const { copied, copy } = useCopy();
  return (
    <div className={cn('cix-code', className)}>
      <pre className={cn(wrap && 'whitespace-pre-wrap break-all')}>{command}</pre>
      <button type="button" onClick={() => void copy(command)} aria-label="Copy to clipboard">
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

// Read-only dark well without an action — code excerpts, log tails, payloads.
export function Well({
  children,
  className,
  ...props
}: React.HTMLAttributes<HTMLPreElement>) {
  return (
    <pre className={cn('cix-well m-0', className)} {...props}>
      {children}
    </pre>
  );
}

// Inline mono chip for a machine value inside a sentence.
export function Chip({ children, className }: { children: React.ReactNode; className?: string }) {
  return <code className={cn('cix-chip', className)}>{children}</code>;
}
