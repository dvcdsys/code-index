# cix-dashboard

The embedded operator dashboard for `cix-server`. Vite + React + TypeScript +
Tailwind + Radix primitives + TanStack Query, served by the Go binary at
`/dashboard` via `embed.FS`.

The UI follows the **cix design system** — cream & ink, described in full
under [Design](#design) below. Read that section before touching any markup;
it is the difference between a change that lands and one that has to be
redone.

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
   import type { Module } from '../types';
   import ProjectsPage from './ProjectsPage';

   export const ProjectsModule: Module = {
     id: 'projects',
     label: 'Projects',
     path: '/projects',
     element: ProjectsPage,
     // requiredRole: 'admin',   // omit for everyone, 'admin' to gate
     group: 'workspace',          // or 'admin' — which sidebar block it sits in
     weight: 10,                  // lower numbers come first within the group
     blurb: 'One sentence for the home-page module grid.',
   };
   ```

   There is no `icon` field. Nav rows carry a 7×7 square marker and the label
   does the work — see the "no icon soup" rule below.

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
- **UI primitives**: import from `@/ui/*` (button, badge, card, input,
  checkbox, code, dialog, alert, page, progress, table, tabs, sonner…).
  Compose those plus the `.cix-*` classes in `index.css`; reach for raw
  Tailwind for layout (flex / grid / gap / spacing) and little else.
- **Icons**: there is no icon library. The chrome has exactly one mark
  (`app/BrandMark.tsx`); everywhere else, state is a 9×9 square plus a word
  and affordances are mono glyphs (`▼ ▾ ▸ ↗ ✕`).
- **Styling**: design tokens only — `bg-surface`, `text-dim`, `border-line-soft`,
  `shadow-hard`, `rounded-card`. Never inline `style={{ color: '#abc' }}`, and
  never a raw hex: colour drift is the reason the token system exists.
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
  index.css             design tokens (light/dark) + the .cix-* component layer
  fonts/                self-hosted JetBrains Mono (400/500/700, no CDN)
  api/
    client.ts           fetch wrapper, ApiError, cookie-based auth
    types.ts            stable re-exports of generated schemas
    generated.ts        ← gitignored; produced by `npm run gen:api`
  app/
    App.tsx             auth-gate + module routing
    Shell.tsx           [banner] [sidebar | main] [full-width status bar]
    Sidebar.tsx         renders modules from the registry, role-filtered
    StatusBar.tsx       the bottom bar + the per-page fact context
    BrandMark.tsx       the one icon in the chrome
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
    home/ projects/ search/ server/ …  one folder per feature
  ui/                   design-system primitives — the whole visual vocabulary
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

`npm run build` should land below ~500 KB gzipped total. Today:

```
index.html                1.7 kB │ gzip:   0.8 kB
assets/index-*.css       53  kB │ gzip:   9.4 kB
assets/index-*.js       617  kB │ gzip: 186   kB
assets/jetbrains-*.woff2 104 kB  (9 subset files, fetched on demand)
```

If a future PR pushes that significantly higher, audit imports — the usual
culprit is a Radix primitive that ships more code than the feature actually
uses.

## Design

Cream & ink. Five rules; everything else follows from them.

1. **Cream, not grey.** The page is `canvas`, panels are `surface`. There is
   no neutral grey — every "grey" is a warm brown (`dim`, `muted`, `faint`).
2. **Ink outlines, not shadows.** Every panel, input, button and badge is
   bounded by a 1.5px ink line. Blur-based shadows do not exist.
3. **Cards are rounded (12px). Controls are square (0).** `borderRadius` is
   overridden globally to `0`, so a stray `rounded-md` renders square instead
   of quietly breaking the look. Only `rounded-card` rounds — and `rounded-full`
   for the radio, the single circle in the system.
4. **Depth is a hard offset shadow.** `shadow-hard` (4px 4px 0) on the one
   primary button per view, on the search bar, on overlays (`shadow-hard-lg`).
   Hover moves the element 2px into its own shadow. Never more than one
   shadowed element per screen region.
5. **Mono for every machine value** — ids, ports, paths, versions, counts,
   timestamps, code, scores, key prefixes. UI sans for prose and buttons.
   Numbers align right; the right edge is the reading axis.

### The tonal ladder

Surfaces are a measured ladder, not a set of moods — one role per step, each a
fixed CIE L\* distance from its neighbour, the same device Material 3 uses for
its `surface-container` roles and Carbon uses for layers. Light theme:

| Role | L\* | Where |
|---|---|---|
| `field` | 97.4 | inputs, selects, the search bar |
| `surface` | 94.4 | card body, table body |
| `canvas` | 89.6 | the page |
| `surface-head` | 84.7 | card header, card footer, table header |

Two rules fall out of it, and both were violated before it was written down:

- **A control a user types into gets `bg-field`, never `bg-surface`.** When the
  input shares its card's fill, a form reads as a grid of identical outlines
  and the eye has nothing to land on; the fill is what says "type here".
- **A header strip must be one full step off the canvas.** `surface-head` used
  to sit 2.0 L\* below the canvas while the body sat 4.8 above it, so card
  headers blended into the page. It is now the canvas's mirror image.

Text tones are a three-level ladder — `ink`, `dim`, `muted` — and **all three
clear WCAG AA (4.5:1) on every surface they are allowed on**. `faint` does not,
by design, and is therefore limited to non-content: placeholders, disabled
text, "never". Check with a contrast tool before introducing a fourth tone;
`muted` shipped at 3.88:1 and made every form label harder to read than it
looked in Figma.

Rank by weight, not by count: three emphasis levels per component is the
budget. A field is *value → label → everything else*, so provenance chips and
recommendations sit at the bottom — a solid ink chip on the least important
fact is what makes a settings page look speckled.

Corollaries worth stating because they are easy to get wrong:

- **Red is a scalpel.** `accent` marks destructive actions, the active nav
  marker, focus rings and the indexing state. Never a decorative fill.
- **No icon soup.** Status is a 9×9 square plus a word — colour never carries
  meaning alone. Use `<Status>`/`<StatusDot>` from `@/ui/badge`.
- **Disabled ≠ information.** Never render read-only facts as greyed-out
  fields or menu items. They go in a key/value block (`<KV>`) or a stat strip.
- **One primary action per page**, in the `<Page>` header. Sub-navigation
  (tabs, segmented controls, filters) belongs in the content area.
- **The status bar spans the full window width**, under the sidebar — it is a
  sibling of the sidebar+main row, never a child of `<main>`. Pages publish
  their middle-slot fact with `useStatusFact()`.

Colours, sizes and geometry live in `src/index.css` as RGB channel vars
(`--c-ink`, …) so the `.dark` class swaps the whole palette and Tailwind can
still apply opacity modifiers. `tailwind.config.ts` maps them to names —
`surface`, `field`, `ink`, `dim`, `muted`, `faint`, `line-soft`, `accent`,
`ok`, `warn`.

### Reviewing a change

`devmock/` (gitignored) is a local visual harness: it installs a mock `fetch`
and boots the real `App`, so every authenticated screen can be reviewed with
realistic data and no server or login.

```bash
npm run dev            # then open /dashboard/devmock.html
```

Before calling a UI change done, sweep for the four usual violations: a
`border-radius` on a control, a `box-shadow` with a blur, a raw grey hex, and
an icon used as the sole carrier of state.
