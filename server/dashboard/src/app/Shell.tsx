import type { ReactNode } from 'react';
import { Sidebar } from './Sidebar';

// Two-column layout: fixed sidebar + flexible main pane. All authenticated
// routes are rendered inside this shell so sidebar state is preserved
// across navigations.
export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-dvh w-full">
      <Sidebar />
      <main className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-5xl px-8 py-8">{children}</div>
      </main>
    </div>
  );
}
