import type { ReactNode } from 'react';
import { BrandMark } from '@/app/BrandMark';

// The frame for the three pages that render outside the app shell: login,
// forced password change, and the bootstrap explainer. A card with an 8px
// hard shadow on the bare canvas — the same weight as a modal, because that
// is exactly what these pages are: one decision, nothing else on screen.
export function AuthShell({
  title,
  subtitle,
  children,
  footer,
  wide,
}: {
  title: string;
  subtitle?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center gap-6 px-4 py-10">
      <div className={wide ? 'w-full max-w-xl' : 'w-full max-w-sm'}>
        <div className="mb-6 flex items-center gap-2.5 font-mono text-[13px] font-bold tracking-[0.08em]">
          <BrandMark />
          <span>cix</span>
          <span className="text-[11px] font-normal tracking-[0.16em] text-muted">DASHBOARD</span>
        </div>

        {/* overflow-hidden is load-bearing: the header strip below is square
            and filled, so without it its corners paint straight over the
            card's 12px arc and the rounded corners read as cut off. */}
        <div className="overflow-hidden rounded-card border bg-surface shadow-hard-lg">
          <div className="border-b bg-surface-head px-5 py-4">
            <h1 className="m-0 text-[20px] font-bold tracking-[-0.01em]">{title}</h1>
            {subtitle ? <p className="mt-1 text-[13.5px] text-dim">{subtitle}</p> : null}
          </div>
          <div className="px-5 py-5">{children}</div>
        </div>

        {footer ? <div className="mt-5 font-mono text-[11.5px] text-muted">{footer}</div> : null}
      </div>
    </div>
  );
}
