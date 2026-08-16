import { useEffect, useState } from 'react';
import { GITHUB_URL, MAC_APP_VERSION } from './versions.js';

// Where the "Download for macOS" button points.
//
// The app is released on its own tag stream (`mac/v*`) and its DMG carries the
// version in its filename, so neither of GitHub's fixed-URL shapes works:
//
//   /releases/latest/download/<asset>  — "latest" is whichever release GitHub
//     flagged Latest, which on this repo is the SERVER stream (server/v* ships
//     far more often than mac/v*). It would hand a Mac visitor a Docker
//     release. It also needs a constant asset name, which a versioned DMG
//     filename is not.
//
// What IS deterministic is the pair `mac/vX.Y.Z` + `cix-X.Y.Z-arm64.dmg`, both
// derived from one version string. So the href is built from MAC_APP_VERSION
// and needs no network and no JavaScript to be correct — and ci-site.yml fails
// the build when that constant drifts from the newest mac/v* tag, which is what
// keeps this half honest.
//
// useMacRelease() then upgrades the link in the browser, running the same query
// the app's own updater does (cli/internal/release/release.go): list releases,
// keep the mac/v* ones, take the highest semver, find the DMG. That makes the
// button follow a release the moment it is published and lets it show the real
// byte size. Every failure path — offline, GitHub's 60-requests-per-hour
// unauthenticated limit, a CSP that forgot api.github.com — silently keeps the
// baked-in link, so the worst case is the version this site was built with.

const REPO = 'dvcdsys/code-index';
const TAG_PREFIX = 'mac/v';
const API = `https://api.github.com/repos/${REPO}/releases?per_page=30`;

// The DMG's filename and its tag, from one version. Both shapes are fixed by
// mac/scripts/make-dmg.sh and release-mac.yml respectively.
export const dmgName = v => `cix-${v}-arm64.dmg`;
export const dmgURL = v => `${GITHUB_URL}/releases/download/${TAG_PREFIX}${v}/${dmgName(v)}`;
export const releaseURL = v => `${GITHUB_URL}/releases/tag/${TAG_PREFIX}${v}`;

// Built-in fallback: what this build of the site knows about.
export const MAC_FALLBACK = {
  version: MAC_APP_VERSION,
  url: dmgURL(MAC_APP_VERSION),
  page: releaseURL(MAC_APP_VERSION),
  size: null,
  live: false,
};

// Numeric semver compare; the streams only ever publish plain X.Y.Z.
function cmp(a, b) {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0);
  }
  return 0;
}

export function formatSize(bytes) {
  if (!bytes) return null;
  return `${(bytes / 1e6).toFixed(1)} MB`;
}

// Resolves the current macOS release, starting from the baked-in one. Fires one
// GET on mount — mount it inside the macOS panel so only visitors who open that
// tab spend the request.
export function useMacRelease() {
  const [rel, setRel] = useState(MAC_FALLBACK);

  useEffect(() => {
    if (typeof fetch !== 'function') return undefined;
    const ac = new AbortController();

    (async () => {
      try {
        const resp = await fetch(API, {
          signal: ac.signal,
          headers: { Accept: 'application/vnd.github+json' },
        });
        if (!resp.ok) return; // 403 rate limit included — keep the fallback
        const releases = await resp.json();
        if (!Array.isArray(releases)) return;

        let best = null;
        for (const r of releases) {
          if (r.draft || r.prerelease) continue;
          if (typeof r.tag_name !== 'string' || !r.tag_name.startsWith(TAG_PREFIX)) continue;
          const version = r.tag_name.slice(TAG_PREFIX.length);
          // Same belt-and-braces filter as the Go client: anything carrying a
          // prerelease or build-metadata suffix is not a release.
          if (/[-+]/.test(version)) continue;
          if (best && cmp(version, best.version) <= 0) continue;
          const dmg = (r.assets || []).find(a => a.name === dmgName(version));
          if (!dmg) continue; // a release without its DMG is not offerable
          best = {
            version,
            url: dmg.browser_download_url,
            page: r.html_url,
            size: dmg.size,
            live: true,
          };
        }
        // Never downgrade: an API answer older than the build is either a
        // pulled release or a stale cache, and the static link still works.
        if (best && cmp(best.version, MAC_FALLBACK.version) >= 0) setRel(best);
      } catch {
        // Offline, blocked, aborted — the fallback link is already rendered.
      }
    })();

    return () => ac.abort();
  }, []);

  return rel;
}
