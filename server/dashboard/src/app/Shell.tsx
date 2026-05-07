import type { ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { Footer } from './Footer';
import { UpdateBanner } from './UpdateBanner';

// Three-row layout: sidebar + main on top, footer spanning the full
// width on the bottom. min-h-0 on the inner row is required so that
// <main>'s overflow-y-auto honors the footer's height when content
// grows tall. UpdateBanner sits above the main row so it spans the
// full width when a newer server release is available.
export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-dvh w-full flex-col">
      <UpdateBanner />
      <div className="flex min-h-0 flex-1">
        <Sidebar />
        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-5xl px-8 py-8">{children}</div>
        </main>
      </div>
      <Footer />
    </div>
  );
}
