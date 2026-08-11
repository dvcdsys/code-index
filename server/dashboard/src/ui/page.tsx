import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

// Every page is: a fixed header (title + one sentence + exactly ONE primary
// action) over a scrolling content area. Sub-navigation — tabs, segmented
// controls, filters — belongs in the content, never in the header.
//
// The header does not scroll away: on the Server page it carries "Save &
// restart", and an action you can lose by scrolling is an action you forget.
export function Page({
  title,
  subtitle,
  action,
  children,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <>
      <header className="cix-pagehead">
        <div className="min-w-0 flex-1">
          <h1>{title}</h1>
          {subtitle ? <p>{subtitle}</p> : null}
        </div>
        {action ? <div className="flex flex-none items-center gap-2.5 pt-1">{action}</div> : null}
      </header>
      <div className="cix-content">{children}</div>
    </>
  );
}

// Section heading inside the content area — mono caps, the same voice as a
// field label, so a page never grows a second competing title style.
export function SectionLabel({
  children,
  aside,
  className,
}: {
  children: ReactNode;
  aside?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('flex items-center justify-between gap-3 pb-2.5', className)}>
      <span className="cix-label">{children}</span>
      {aside}
    </div>
  );
}
