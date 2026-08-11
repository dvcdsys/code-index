import { useEffect, useRef } from 'react';
import { cn } from '@/lib/cn';

// The hero of the page: h48, hard shadow, a blocky magnifier drawn in SVG
// (squares and a straight handle, matching the brand mark) and a ⌘K chip on
// the right. It is the only shadowed element in this region.
export function SearchBar({
  value,
  onChange,
  onSubmit,
  placeholder = 'Search…',
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  /** Fired on Enter — bypasses the debounce and commits immediately. */
  onSubmit?: (v: string) => void;
  placeholder?: string;
  className?: string;
}) {
  const ref = useRef<HTMLInputElement | null>(null);

  // ⌘K / Ctrl-K focuses the bar from anywhere on the page.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const mac = navigator.platform.toLowerCase().includes('mac');
      if ((mac ? e.metaKey : e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        ref.current?.focus();
        ref.current?.select();
      }
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <form
      className={cn('cix-search', className)}
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit?.(value);
      }}
      role="search"
    >
      <svg viewBox="0 0 16 16" className="h-4 w-4 flex-none" aria-hidden>
        <path
          d="M2 2h9v9H2z"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          shapeRendering="crispEdges"
        />
        <path d="M10.5 10.5 14.5 14.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="square" />
      </svg>
      <input
        ref={ref}
        type="text"
        autoFocus
        spellCheck={false}
        autoComplete="off"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label="Search query"
      />
      <kbd className="cix-kbd hidden select-none sm:inline-block" aria-hidden>
        ⌘K
      </kbd>
    </form>
  );
}
