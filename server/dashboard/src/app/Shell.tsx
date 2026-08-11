import type { ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { StatusBar } from './StatusBar';
import { UpdateBanner } from './UpdateBanner';

// The app is a column: [banner] [sidebar | main] [status bar].
//
// The status bar is a SIBLING of the sidebar+main row, not a child of main —
// it spans the full window width and runs under the sidebar. That is the one
// structural rule of the shell; nesting it inside <main> is the bug this
// layout exists to prevent.
//
// Pages render their own <Page> (header + scrolling content) into <main>, so
// the page header stays pinned while the content below it scrolls.
export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="cix-app">
      <UpdateBanner />
      <div className="cix-body">
        <Sidebar />
        <main className="cix-main">{children}</main>
      </div>
      <StatusBar />
    </div>
  );
}
