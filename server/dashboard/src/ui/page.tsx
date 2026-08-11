import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

// Every page is: a fixed header (title + one sentence + at most ONE primary
// action) over a scrolling content area. Sub-navigation — tabs, segmented
// controls, filters — belongs in the content, never in the header.
//
// The header does not scroll away, which is why an action lives there: one you
// can lose by scrolling is one you forget. But that only holds while the action
// applies to the whole page. Once a page grows tabs, a header action either
// follows the active tab — appearing and vanishing under the title, which reads
// as a glitch — or hovers over tabs it has nothing to do with. The Server page
// hit exactly that and moved "Save & restart" into its Runtime settings tab,
// as a plain bar closing the form it acts on. Tabbed pages should follow suit.
//
// That bar was sticky at first, to keep the "cannot scroll it away" property.
// It was worse: cards sliding under a pinned strip read as a rendering fault.
// A form short enough to reach the end of does not need its action pinned.
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
