# Plan: Prefixed Git Tags + Server Versioning + Compatibility Check

## Context

Currently only CLI is versioned (via ldflags at build time, triggered by `v*` tag). Server version is hardcoded in `main.py` and `pyproject.toml`, not connected to git tags. No version compatibility check between client and server. Need independent versioning for CLI and server with compatibility validation.

## Tag Format

```
cli/v0.2.0      — CLI release
server/v0.2.0   — Server/API release
```

Both follow semver. CLI and server can be released independently.

## Compatibility Protocol

Use **API version** as compatibility contract. Both client and server declare which API version they support (currently `v1`). As long as API version matches — they're compatible.

Additionally, server returns its version in `/api/v1/status` so CLI can display it and warn on mismatch.

---

## Step 1: Server Version as Hardcoded Constant

**New file: `api/app/version.py`** — single source of truth:

```python
SERVER_VERSION = "0.2.0"
API_VERSION = "v1"
```

**File: `api/app/main.py`** — import from version module:

```python
from .version import SERVER_VERSION

app = FastAPI(
    title="Claude Code Index API",
    version=SERVER_VERSION,
)
```

At release time, update the constant in `version.py`. GitHub Actions validates that git tag `server/v0.2.0` matches the constant in code (see Step 4b).

## Step 2: Expose Server Version in `/api/v1/status`

**File: `api/app/routers/health.py`**

Add `server_version` and `api_version` to status response:

```python
from ..version import SERVER_VERSION, API_VERSION

@router.get("/api/v1/status", dependencies=[Depends(verify_api_key)])
async def status():
    ...
    return {
        "status": "ok",
        "server_version": SERVER_VERSION,
        "api_version": API_VERSION,
        "model_loaded": True,
        "projects": project_count,
        "active_indexing_jobs": active_jobs,
    }
```

## Step 3: CLI Sends Version, Checks Compatibility

**File: `cli/internal/client/client.go`**

Add `X-Client-Version` header to all requests:

```go
func New(baseURL, apiKey, version string) *Client { ... }

// in do():
req.Header.Set("X-Client-Version", c.version)
```

Update `StatusResponse` struct:

```go
type StatusResponse struct {
    Status             string `json:"status"`
    ServerVersion      string `json:"server_version"`
    APIVersion         string `json:"api_version"`
    ModelLoaded        bool   `json:"model_loaded"`
    Projects           int    `json:"projects"`
    ActiveIndexingJobs int    `json:"active_indexing_jobs"`
}
```

**File: `cli/cmd/root.go`**

Pass `Version` to client constructor.

Add version check on `cix status`:
```go
if status.APIVersion != "" && status.APIVersion != "v1" {
    fmt.Fprintf(os.Stderr, "⚠ Server API %s, client expects v1. Update cix.\n", status.APIVersion)
}
```

## Step 4: GitHub Actions — Separate Workflows

### 4a. Rename existing workflow

**File: `.github/workflows/release-cli.yml`**

Change trigger from `v*` to `cli/v*`:

```yaml
on:
  push:
    tags:
      - "cli/v*"
```

Update version extraction:
```yaml
- name: Build
  run: |
    VERSION="${{ github.ref_name }}"        # cli/v0.2.0
    VERSION="${VERSION#cli/}"               # v0.2.0
    go build -ldflags="-X '...Version=${VERSION}'" ...
```

Release title uses tag as-is: `cli/v0.2.0`.

### 4b. New server workflow

**File: `.github/workflows/release-server.yml`**

```yaml
name: Release Server

on:
  push:
    tags:
      - "server/v*"

jobs:
  docker:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Extract version
        run: echo "VERSION=${GITHUB_REF_NAME#server/}" >> $GITHUB_ENV

      - name: Validate version matches code
        run: |
          CODE_VERSION=$(grep 'SERVER_VERSION' api/app/version.py | head -1 | sed 's/.*"\(.*\)".*/\1/')
          if [ "${{ env.VERSION }}" != "v${CODE_VERSION}" ]; then
            echo "ERROR: Tag ${{ env.VERSION }} != code version v${CODE_VERSION}"
            echo "Update SERVER_VERSION in api/app/version.py first"
            exit 1
          fi

      - name: Login to Docker Hub
        uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKER_USERNAME }}
          password: ${{ secrets.DOCKER_PASSWORD }}

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Build and push (multi-arch)
        uses: docker/build-push-action@v6
        with:
          context: .
          file: api/Dockerfile
          platforms: linux/arm64,linux/amd64
          push: true
          tags: |
            dvcdsys/code-index:${{ env.VERSION }}
            dvcdsys/code-index:latest

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
```

Optional: add CUDA variant job in the same workflow.

## Step 5: Update Makefiles

**File: `cli/Makefile`** — no changes needed, already uses `VERSION`.

**File: `Makefile` (root)** — split VERSION into two vars for clarity:

```makefile
CLI_VERSION    ?= v0.2.0
SERVER_VERSION ?= v0.2.0
```

Docker targets use `SERVER_VERSION` for image tags (version is already baked into the code, no build-arg needed).

## Step 6: Update install.sh

**File: `install.sh`**

Change to fetch only `cli/*` releases:

```bash
# Instead of fetching "latest", fetch latest cli/* tag
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep '"tag_name"' \
    | grep 'cli/' \
    | head -1 \
    | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
# Strip prefix for archive name
CLEAN_VERSION="${VERSION#cli/}"
```

## Step 7: Update docker-compose.yml

No changes needed — version is hardcoded in `api/app/version.py`, not injected via env.

---

## Release Workflow (for humans)

### CLI release:
```bash
git tag cli/v0.2.0
git push origin cli/v0.2.0
# → GitHub Actions builds binaries, creates release
```

### Server release:
```bash
git tag server/v0.2.0
git push origin server/v0.2.0
# → GitHub Actions builds & pushes Docker images, creates release
```

### Both together:
```bash
git tag cli/v0.2.0 && git tag server/v0.2.0
git push origin cli/v0.2.0 server/v0.2.0
```

---

## Files to Modify

| File                                     | Change                                                        |
|------------------------------------------|---------------------------------------------------------------|
| `api/app/version.py`                     | **New** — `SERVER_VERSION` and `API_VERSION` constants         |
| `api/app/main.py`                        | Import `SERVER_VERSION` from `version.py`                     |
| `api/app/routers/health.py`              | Add `server_version`, `api_version` to `/api/v1/status`       |
| `cli/internal/client/client.go`          | Add version field, send `X-Client-Version` header             |
| `cli/cmd/root.go`                        | Pass version to client, warn on API mismatch                  |
| `.github/workflows/release-cli.yml`      | Change trigger to `cli/v*`, strip prefix in version extraction |
| `.github/workflows/release-server.yml`   | **New** — validate version, build & push Docker on `server/v*` |
| `Makefile`                               | Split `VERSION` into `CLI_VERSION` / `SERVER_VERSION`          |
| `install.sh`                             | Filter for `cli/*` releases                                   |

## Verification

1. `git tag cli/v0.2.0 && git push origin cli/v0.2.0` — check GitHub Actions builds CLI
2. `git tag server/v0.2.0 && git push origin server/v0.2.0` — check Docker push + version validation
3. `cix status` — should show `server_version` and `api_version`
4. Start server locally — verify `/api/v1/status` returns `"server_version": "0.2.0"`
5. Install via `install.sh` — verify it picks up `cli/v*` releases
6. Try pushing tag `server/v0.3.0` without updating `version.py` — workflow should fail