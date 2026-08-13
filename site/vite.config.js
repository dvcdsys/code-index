import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// fileURLToPath instead of import.meta.dirname: the latter is undefined on
// Node < 20.11 and fails with an unrelated-looking TypeError from resolve().
const here = dirname(fileURLToPath(import.meta.url));

// Multi-page static site: the landing page and the docs page are separate
// HTML entry points (no SPA router, no fallback needed on Cloudflare Pages).
export default defineConfig({
  plugins: [react()],
  build: {
    // Cache generation, bumped to evacuate a poisoned URL. On 2026-08-10 a
    // deploy window answered requests for /assets/*.js with index.html and a
    // 200, and public/_headers stamped that HTML with the year-long immutable
    // Cache-Control meant for hashed bundles — so every browser that hit the
    // site during the window pinned "HTML that claims to be JavaScript" until
    // 2027 and rendered a blank page. Content hashes cannot rescue those
    // clients (the poisoned entry is keyed by the URL, and the URL was right),
    // so the whole directory moves instead; index.html is served
    // must-revalidate and picks the new paths up immediately.
    // public/404.html now makes a missing asset a real 404, which is what
    // stops this from recurring — bump this only if it somehow happens again.
    assetsDir: 'assets/g2',
    rollupOptions: {
      input: {
        main: resolve(here, 'index.html'),
        docs: resolve(here, 'docs/index.html'),
      },
    },
  },
});
