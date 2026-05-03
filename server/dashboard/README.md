# cix-dashboard

The embedded operator dashboard for `cix-server`. Vite + React + TypeScript +
Tailwind + shadcn/ui + TanStack Query, served by the Go binary at
`/dashboard` via `embed.FS`.

## Local development

```bash
# one-time
cd server/dashboard
npm ci

# regenerate API types when doc/openapi.yaml changes
npm run gen:api

# vite dev server on http://localhost:5173 (proxies /api → :21847)
npm run dev

# production build → ../internal/httpapi/dashboard/dist
npm run build
```

The repo Makefile wraps the same targets:

```bash
cd server
make dashboard-dev     # vite dev server with type-gen
make dashboard-build   # production build
make build             # rebuild Go binary with the latest dashboard embedded
```

## How to add a new feature module

A "feature" is a self-contained folder under `src/modules/<name>/` that
exports a single `Module` constant. The sidebar and the router both read
from `src/modules/registry.ts` — adding a feature is **create folder,
register, done**.

1. **Create the module folder**:

   ```
   src/modules/projects/
     index.ts         # exports the Module
     ProjectsPage.tsx # the entry component
     hooks.ts         # TanStack Query hooks (useProjects(), …)
     components/      # local components, never imported from outside
   ```

2. **Define the module**:

   ```ts
   // src/modules/projects/index.ts
   import { Folder } from 'lucide-react';
   import type { Module } from '../types';
   import ProjectsPage from './ProjectsPage';

   export const ProjectsModule: Module = {
     id: 'projects',
     label: 'Projects',
     icon: Folder,
     path: '/projects',
     element: ProjectsPage,
     // requiredRole: 'admin',   // omit for everyone, 'admin' to gate
     weight: 10,                  // lower numbers come first in the sidebar
   };
   ```

3. **Register it**:

   ```ts
   // src/modules/registry.ts
   import { ProjectsModule } from './projects';

   export const MODULES: Module[] = [HomeModule, ProjectsModule, …]
     .sort((a, b) => (a.weight ?? 100) - (b.weight ?? 100));
   ```

   That's it. The sidebar renders the new entry, role-filtered by the
   current user; the router mounts `<Route path={path+'/*'} element={element}/>`
   so the module owns its sub-tree.

4. **If the module needs new endpoints** — add them to `doc/openapi.yaml`
   first, then run:
   ```bash
   cd server && make openapi-gen          # Go server stub
   cd dashboard && npm run gen:api        # TS types in src/api/generated.ts
   ```
   Both generators are idempotent and CI-checked, so a forgotten
   regeneration fails fast.

## Conventions

- **Data fetching**: every API call goes through TanStack Query
  (`useQuery` / `useMutation`). Never raw `useEffect + fetch` — that
  loses retry, cache, and dedupe for free.
- **API client**: import `api` from `@/api/client`. Returns parsed JSON
  on success, throws an `ApiError` (with `.status` and `.detail`) on any
  non-2xx. The provider in `app/providers.tsx` already disables retries
  on 401/403.
- **UI primitives**: import from `@/ui/*` (button, card, input, dialog,
  alert, sonner). All wrap shadcn-style Tailwind primitives. Add new
  ones via `npx shadcn add <name>` when needed.
- **Icons**: `lucide-react` only. Named imports — no default re-exports
  so the bundle tree-shakes.
- **Styling**: Tailwind tokens only (`bg-background`, `text-muted-foreground`,
  …). Never inline `style={{ color: '#abc' }}` — colour drift is the
  reason we have a token system.
- **Class strings**: use `cn()` from `@/lib/cn` for conditional classes.
  It de-duplicates conflicting Tailwind classes.
- **Dates**: format via helpers in `@/lib/formatDate`. Don't sprinkle
  `new Date(x).toLocaleDateString()` across the codebase.
- **Auth state**: `useAuth()` from `@/auth/useAuth`. Don't read the
  `/auth/me` query directly — the hook is the public surface.

## Architecture at a glance

```
src/
  main.tsx              boot React + Router + providers
  index.css             Tailwind + shadcn CSS variables (light/dark tokens)
  api/
    client.ts           fetch wrapper, ApiError, cookie-based auth
    types.ts            stable re-exports of generated schemas
    generated.ts        ← gitignored; produced by `npm run gen:api`
  app/
    App.tsx             auth-gate + module routing
    Shell.tsx           sidebar + main content layout
    Sidebar.tsx         renders modules from the registry, role-filtered
    providers.tsx       QueryClient + AuthProvider + Toaster
  auth/
    AuthProvider.tsx    bootstrap-status + /auth/me + login/logout mutations
    useAuth.ts          consumer hook
    LoginPage.tsx       full-page (no Shell)
    ChangePasswordPage.tsx   forced password change
    BootstrapNeededPage.tsx  shown when `needs_bootstrap === true`
  modules/
    types.ts            Module interface
    registry.ts         array of all registered modules, sorted by weight
    home/               PR-B placeholder home; replace/augment in PR-C
  ui/                   shadcn primitives — never edit unless adding a new one
  lib/
    cn.ts               `cn()` className helper
    formatDate.ts       date / relative-time helpers
```

## Embedding into the Go binary

The Vite build writes its output to
`server/internal/httpapi/dashboard/dist/`, which is referenced by
`//go:embed all:dist` in `dashboard/embed.go`. After `make dashboard-build`
finishes, a regular `go build` picks the bundle up automatically.

A committed `dist/.gitkeep` keeps the embed.FS non-empty on a fresh clone
so `go build` works without the npm toolchain. The handler in
`dashboard.go` serves an inline "please run `make dashboard-build`"
placeholder when `dist/index.html` is missing.

## Bundle-size budget

`npm run build` should land below ~500 KB gzipped total. Today (PR-B):

```
index.html                0.5 kB │ gzip:  0.3 kB
assets/index-*.css       18  kB │ gzip:  4.3 kB
assets/index-*.js       289  kB │ gzip: 90  kB
```

If a future PR pushes that significantly higher, audit imports — the
usual culprits are accidentally pulling all of lucide-react instead of
named imports, or shadcn primitives that ship more Radix code than the
feature actually uses.
