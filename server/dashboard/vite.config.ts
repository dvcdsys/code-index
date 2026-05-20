import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { writeFileSync } from 'node:fs';
import path from 'node:path';

const OUT_DIR = '../internal/httpapi/dashboard/dist';

// keepGitkeep re-creates dist/.gitkeep after each build. `emptyOutDir: true`
// (required below) wipes the whole output dir, including the committed
// `.gitkeep` marker that keeps dist/ tracked in git for Go's
// `//go:embed all:dist`. Without this, every `vite build` shows up as a
// deleted .gitkeep — the marker keeps leaking out of commits and breaks
// `go build`/CI on a fresh clone where dist/ holds only the marker. Runs in
// `closeBundle`, which fires once the bundle is fully written.
function keepGitkeep(outDir: string): Plugin {
  return {
    name: 'cix-keep-gitkeep',
    closeBundle() {
      writeFileSync(path.resolve(__dirname, outDir, '.gitkeep'), '');
    },
  };
}

// Vite is told two non-default things:
//  1. base: '/dashboard/' — the Go server mounts the SPA under that prefix
//     so all asset URLs need to be rewritten accordingly.
//  2. build.outDir: ../internal/httpapi/dashboard/dist — output lands inside
//     the Go embed.FS root so `go build` picks it up automatically.
//
// In dev (`npm run dev`), /api requests are proxied to the running Go server
// on the default cix-server port (21847) so cookie auth works through the
// dev server origin.
export default defineConfig({
  plugins: [react(), keepGitkeep(OUT_DIR)],
  base: '/dashboard/',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:21847',
        changeOrigin: false,
        secure: false,
      },
    },
  },
  build: {
    outDir: OUT_DIR,
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        // Stable hashed names; Go fileserver caches via Cache-Control.
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
});
