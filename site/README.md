# codeindex.app — marketing & docs site

Static two-page site (landing + `/docs/`) for **cix — CodeIndeX**, built with
Vite + React and deployed to Cloudflare Pages.

```bash
cd site
npm install
npm run dev        # local dev server
npm run build      # production build → dist/
npm run preview    # serve the built dist/ locally
```

## Deployment (Cloudflare Pages)

- Root directory: `site`
- Build command: `npm run build`
- Output directory: `dist`
- Production branch: `main` (other branches / PRs get preview URLs)
- Custom domain: `codeindex.app` (canonical). `code-index.app` 301-redirects
  to it via a zone-level Redirect Rule (Pages `_redirects` cannot redirect
  across domains).

Headers (caching + CSP) live in [`public/_headers`](public/_headers).

## Content rules

1. **Never state a fact you can't point to in the repo.** Every number on the
   site (language count, versions, flags, endpoints, scores) was verified
   against the codebase when written; keep it that way. The terminal demos are
   transcribed from real `cix` runs against this repository — if you change
   them, run the command and transcribe, don't invent.
2. **Versions are hand-maintained** in
   [`src/shared/versions.js`](src/shared/versions.js). Bump on each
   server/CLI/plugin release. `MAC_APP_VERSION` is load-bearing rather than
   decorative: the Quick start's download button builds its href from it
   (`releases/download/mac/v$V/cix-$V-arm64.dmg`), and that static link is what
   every visitor gets whenever the GitHub API lookup in
   [`src/shared/mac-release.js`](src/shared/mac-release.js) is rate limited,
   blocked or offline. `ci-site.yml` fails the build if any of the four drifts
   from the newest tag.
3. **Brand:** the full product name is **CodeIndeX** (one word, capital C-I-X);
   `cix` is the CLI command and short form. First mention on any surface pairs
   both: “cix — CodeIndeX”. Domain `codeindex.app`, repo `code-index` are the
   same name in medium-specific spelling.

## OG image

`public/og-image.png` (1200×630) is rendered from [`og/og.html`](og/og.html)
via headless Chrome. To regenerate after editing the card:

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless --screenshot=public/og-image.png --window-size=1200,630 \
  --hide-scrollbars "file://$PWD/og/og.html"
```
