import { cn } from '@/lib/cn';

// The one icon in the chrome: the cix magnifier, drawn as squares and a
// straight handle so it matches the app icon and the menu-bar panel. Inline
// SVG (not a font glyph, not lucide) because it is brand, not iconography —
// currentColor drives the ring so it inverts cleanly on an ink surface.
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 16 16"
      className={cn('h-4 w-4 flex-none', className)}
      aria-hidden
      focusable="false"
    >
      <path
        d="M2 2h8v8H2z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.75"
        shapeRendering="crispEdges"
      />
      <path
        d="M9.5 9.5 14 14"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="square"
      />
      <rect x="4" y="4" width="4" height="4" fill="rgb(var(--c-accent))" />
    </svg>
  );
}
