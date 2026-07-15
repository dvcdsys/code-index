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
    rollupOptions: {
      input: {
        main: resolve(here, 'index.html'),
        docs: resolve(here, 'docs/index.html'),
      },
    },
  },
});
