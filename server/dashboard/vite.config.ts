import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

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
  plugins: [react()],
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
    outDir: '../internal/httpapi/dashboard/dist',
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
