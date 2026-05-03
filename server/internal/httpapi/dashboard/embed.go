// Package dashboard holds the React SPA bundle that is served at /dashboard.
//
// The bundle is produced by `cd server && make dashboard-build` (which in
// turn runs `npm run build` inside server/dashboard/). Vite output lands in
// this directory's dist/ tree:
//
//	dist/index.html
//	dist/assets/index-<hash>.js
//	dist/assets/index-<hash>.css
//
// On a fresh clone, dist/ contains only the committed `.gitkeep` marker —
// the real build artefacts are gitignored. The `all:` prefix in the embed
// directive includes dotfiles so the embed.FS is non-empty even before the
// frontend has been built. dashboard.go serves a hardcoded "please build"
// placeholder when index.html is missing.
package dashboard

import "embed"

//go:embed all:dist
var Assets embed.FS
