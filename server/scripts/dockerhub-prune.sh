#!/usr/bin/env bash
# dockerhub-prune.sh — delete stale tags from the Docker Hub repository so
# storage stops growing without bound. Docker Hub never prunes tags itself;
# every release adds ~1 GB (CUDA) and old versions/scan tags pile up forever.
#
# Retention policy (deny-by-default — anything not explicitly kept is deleted):
#   KEEP  floating tags         : $PROTECT_FLOATING (default: latest cu128 develop-cu128)
#   KEEP  extra pinned tags     : $PROTECT_EXTRA     (default: none)
#   KEEP  release tags          : the $KEEP_RELEASES newest semver versions,
#                                 both `vX.Y.Z` and `vX.Y.Z-cu128` variants
#   DELETE everything else      : scout-*, *-cu130/-cu126, go-*, *-dev*,
#                                 ci-test-*, gguf-test-*, unprefixed 0.x, and
#                                 any release older than the keep window.
#
# A tag counts as a "release" only if it matches  ^v[0-9]+\.[0-9]+\.[0-9]+(-cu128)?$
# — the current tag scheme. Legacy/experimental tags fall through to DELETE,
# which is the point: this is how scout/test/cu130 cruft gets reaped.
#
# SAFETY: DRY_RUN defaults to "true" — it prints the keep/delete plan and exits
# without touching anything. You must pass DRY_RUN=false to actually delete.
# Deleted release tags can always be rebuilt from their git tag via the
# release-server.yml `workflow_dispatch` path, so this is recoverable.
#
# Auth: Docker Hub API JWT obtained from username + a Personal Access Token
# (PAT). In CI these come from the same secrets the build uses.
#
# Usage:
#   DOCKERHUB_USERNAME=... DOCKERHUB_TOKEN=<PAT> \
#     server/scripts/dockerhub-prune.sh                 # preview (dry-run)
#   DRY_RUN=false DOCKERHUB_USERNAME=... DOCKERHUB_TOKEN=<PAT> \
#     server/scripts/dockerhub-prune.sh                 # actually delete
#
# Env vars (all optional except auth):
#   DOCKERHUB_USERNAME / DOCKER_USERNAME   Hub account (required)
#   DOCKERHUB_TOKEN    / DOCKER_PASSWORD   Hub PAT or password (required)
#   NAMESPACE          (default: dvcdsys)
#   REPO               (default: code-index)
#   KEEP_RELEASES      (default: 3)
#   PROTECT_FLOATING   (default: "latest cu128 develop-cu128")
#   PROTECT_EXTRA      (default: "")  space/comma-separated extra tags to keep
#   DRY_RUN            (default: true)  set to "false" to delete
set -euo pipefail

NAMESPACE="${NAMESPACE:-dvcdsys}"
REPO="${REPO:-code-index}"
KEEP_RELEASES="${KEEP_RELEASES:-3}"
PROTECT_FLOATING="${PROTECT_FLOATING:-latest cu128 develop-cu128}"
PROTECT_EXTRA="${PROTECT_EXTRA:-}"
DRY_RUN="${DRY_RUN:-true}"

USERNAME="${DOCKERHUB_USERNAME:-${DOCKER_USERNAME:-}}"
TOKEN="${DOCKERHUB_TOKEN:-${DOCKER_PASSWORD:-}}"

if [ -z "$USERNAME" ] || [ -z "$TOKEN" ]; then
  echo "ERROR: set DOCKERHUB_USERNAME and DOCKERHUB_TOKEN (a Docker Hub PAT)." >&2
  exit 2
fi
command -v jq   >/dev/null || { echo "ERROR: jq is required." >&2; exit 2; }
command -v curl >/dev/null || { echo "ERROR: curl is required." >&2; exit 2; }

API="https://hub.docker.com/v2"

# --- authenticate -----------------------------------------------------------
JWT="$(curl -fsS -H 'Content-Type: application/json' -X POST \
  -d "$(jq -n --arg u "$USERNAME" --arg p "$TOKEN" '{username:$u, password:$p}')" \
  "$API/users/login/" | jq -r '.token')"
if [ -z "$JWT" ] || [ "$JWT" = "null" ]; then
  echo "ERROR: Docker Hub login failed (check username / PAT)." >&2
  exit 1
fi
auth=(-H "Authorization: JWT $JWT")

# --- collect every tag (paginated) ------------------------------------------
tags=()
url="$API/repositories/$NAMESPACE/$REPO/tags/?page_size=100"
while [ -n "$url" ] && [ "$url" != "null" ]; do
  page="$(curl -fsS "${auth[@]}" "$url")"
  while IFS= read -r t; do [ -n "$t" ] && tags+=("$t"); done \
    < <(echo "$page" | jq -r '.results[].name')
  url="$(echo "$page" | jq -r '.next')"
done
echo "Found ${#tags[@]} tags in $NAMESPACE/$REPO"

# --- normalise protect lists to newline-delimited for grep -Fx -------------
protect_list="$(printf '%s %s' "$PROTECT_FLOATING" "$PROTECT_EXTRA" \
  | tr ',' ' ' | tr ' ' '\n' | grep -v '^$' || true)"
is_protected() { grep -Fxq "$1" <<<"$protect_list"; }

release_re='^v[0-9]+\.[0-9]+\.[0-9]+(-cu128)?$'

# --- determine the KEEP_RELEASES newest release versions --------------------
declare -a release_versions=()
for t in "${tags[@]}"; do
  if [[ "$t" =~ $release_re ]]; then
    release_versions+=("${t%-cu128}")   # strip flavor → base vX.Y.Z
  fi
done
keep_versions=""
if [ "${#release_versions[@]}" -gt 0 ]; then
  # Portable semver sort (BSD + GNU): strip the leading v, sort numerically by
  # dotted fields descending, keep the top N, re-add the v. Avoids `sort -V`,
  # which macOS BSD sort lacks.
  keep_versions="$(printf '%s\n' "${release_versions[@]}" \
    | sed 's/^v//' \
    | sort -u -t. -k1,1nr -k2,2nr -k3,3nr \
    | head -n "$KEEP_RELEASES" \
    | sed 's/^/v/')"
fi
is_kept_release() { grep -Fxq "$1" <<<"$keep_versions"; }

# --- classify ---------------------------------------------------------------
keep=()
delete=()
for t in "${tags[@]}"; do
  if is_protected "$t"; then
    keep+=("$t")
  elif [[ "$t" =~ $release_re ]] && is_kept_release "${t%-cu128}"; then
    keep+=("$t")
  else
    delete+=("$t")
  fi
done

echo ""
echo "KEEP (${#keep[@]}):"
printf '  %s\n' "${keep[@]}" | sort
echo ""
echo "DELETE (${#delete[@]}):"
printf '  %s\n' "${delete[@]}" | sort

if [ "$DRY_RUN" != "false" ]; then
  echo ""
  echo "DRY_RUN=$DRY_RUN — nothing deleted. Re-run with DRY_RUN=false to apply."
  exit 0
fi

# --- delete -----------------------------------------------------------------
echo ""
echo "Deleting ${#delete[@]} tags..."
fail=0
for t in "${delete[@]}"; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" -X DELETE \
    "$API/repositories/$NAMESPACE/$REPO/tags/$t/")"
  if [ "$code" = "204" ] || [ "$code" = "202" ] || [ "$code" = "200" ]; then
    echo "  deleted  $t  ($code)"
  else
    echo "  FAILED   $t  ($code)" >&2
    fail=$((fail + 1))
  fi
done
echo ""
echo "Done. Deleted $(( ${#delete[@]} - fail ))/${#delete[@]} tags ($fail failures)."
[ "$fail" -eq 0 ]
