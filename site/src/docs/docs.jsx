import { useState, useEffect } from 'react';
import { CixAscii } from '../shared/ascii.jsx';
import { Foot } from '../shared/foot.jsx';
import {
  SERVER_VERSION, CLI_VERSION, PLUGIN_VERSION, COWORK_PLUGIN_VERSION, GITHUB_URL,
} from '../shared/versions.js';

const TOC = [
  { id: 'overview',     label: 'Overview' },
  { id: 'install',      label: 'Install' },
  { id: 'cli',          label: 'CLI reference' },
  { id: 'tuning',       label: 'Tuning search' },
  { id: 'config',       label: 'Configuration' },
  { id: 'api',          label: 'REST API' },
  { id: 'workspaces',   label: 'Workspaces' },
  { id: 'mcp',          label: 'MCP · Desktop & Cowork' },
  { id: 'plugin',       label: 'Claude Code plugin' },
  { id: 'github',       label: 'GitHub repos & tunnels' },
  { id: 'security',     label: 'Security & access' },
  { id: 'troubleshoot', label: 'Troubleshooting' },
];

export function DocsApp() {
  const [active, setActive] = useState('overview');

  useEffect(() => {
    const obs = new IntersectionObserver(entries => {
      entries.forEach(e => { if (e.isIntersecting) setActive(e.target.id); });
    }, { rootMargin: '-30% 0px -55% 0px' });
    TOC.forEach(t => { const el = document.getElementById(t.id); if (el) obs.observe(el); });
    return () => obs.disconnect();
  }, []);

  return (
    <>
      <header className="nav">
        <div className="wrap nav-inner">
          <a className="nav-logo" href="/" aria-label="cix — CodeIndeX, home">
            <CixAscii withTag />
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--ink-mute)', marginLeft: 6, letterSpacing: '0.1em' }}>/ docs</span>
          </a>
          <nav className="nav-links">
            <a href="/">Home</a>
            <a className="nav-sec" href="/#features">Features</a>
            <a href={GITHUB_URL} target="_blank" rel="noopener">GitHub ↗</a>
          </nav>
        </div>
      </header>

      <main className="docs-layout">
        <aside className="docs-sidebar">
          <div className="docs-sidebar-inner">
            <span className="eyebrow" style={{ marginBottom: 18 }}>On this page</span>
            <ul className="toc">
              {TOC.map(t => (
                <li key={t.id} className={t.id === active ? 'on' : ''}>
                  <a href={`#${t.id}`}>{t.label}</a>
                </li>
              ))}
            </ul>
            <div style={{ marginTop: 32, padding: '14px 16px', background: 'var(--paper)', border: '2px solid var(--ink)', borderRadius: 14, fontSize: 13 }}>
              <b>Need help?</b>
              <div style={{ color: 'var(--ink-soft)', marginTop: 4 }}>
                Open an <a href={`${GITHUB_URL}/issues`} target="_blank" rel="noopener">issue on GitHub</a>.
              </div>
            </div>
          </div>
        </aside>

        <article className="docs-main">
          <div className="docs-hero">
            <span className="eyebrow">Documentation</span>
            <h1>cix docs</h1>
            <p className="lead">cix (CodeIndeX) — everything you need to run a self-hosted semantic index for your code. Deploy, configure, and integrate with agents.</p>
            <div className="badge-strip">
              <span className="sticker">server v{SERVER_VERSION}</span>
              <span className="sticker" style={{ background: 'var(--ochre)' }}>CLI v{CLI_VERSION}</span>
              <span className="sticker">MIT</span>
              <span className="sticker">31 languages</span>
            </div>
          </div>

          <Section id="overview" title="Overview" eyebrow="Concepts">
            <p>cix is a single Go binary that exposes an HTTP API and a web dashboard at <code>http://host:21847</code>. It chunks your code with tree-sitter (31 grammars, run in a WASM sandbox), embeds chunks via a local <code>llama-server</code> sidecar, stores vectors in chromem-go, mirrors chunk text into a SQLite FTS5 table for BM25 scoring, and keeps symbols + project metadata in SQLite. Five search modes run on top: semantic, symbols, definitions, references, and files — plus hybrid cross-repo search over <a href="#workspaces">workspaces</a>.</p>
            <h3>The three components</h3>
            <ul className="bullets">
              <li><b>cix-server</b> — the Go binary. Embedded React dashboard. Embedded Swagger UI at <code>/docs</code>. Ships as a distroless container.</li>
              <li><b>llama-server sidecar</b> — a bundled llama.cpp child process that loads CodeRankEmbed-Q8 (GGUF) and answers embedding requests over a Unix socket.</li>
              <li><b>cix CLI</b> — a Go binary you install locally. <code>cix init</code>, <code>cix search</code>, <code>cix watch</code>. The CLI is what your agent calls.</li>
            </ul>
            <Diagram />
          </Section>

          <Section id="install" title="Install" eyebrow="Quick start">
            <h3>Server (Docker, CPU)</h3>
            <CodeBlock>{`git clone https://github.com/dvcdsys/code-index && cd code-index
cp .env.example .env
# Edit .env: CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
docker compose up -d
curl http://localhost:21847/health   # → {"status":"ok"}`}</CodeBlock>

            <h3>Server (CUDA / NVIDIA GPU)</h3>
            <CodeBlock>{`docker compose -f docker-compose.cuda.yml up -d`}</CodeBlock>
            <p>Requires an NVIDIA driver ≥ 525 (CUDA 12.x) and the NVIDIA Container Toolkit. The CUDA image sets <code>CIX_N_GPU_LAYERS=99</code> for full GPU offload.</p>

            <h3>Server (native macOS, Apple Silicon)</h3>
            <p>Docker on macOS can't reach the Metal GPU. For full Metal offload, run natively — on macOS the server already defaults to offloading all layers, no extra configuration needed:</p>
            <CodeBlock>{`xcode-select --install              # if not installed
cd server && make bundle             # builds server + downloads Metal-enabled llama-server
cp ../.env.example ../.env           # set the bootstrap admin vars
make run`}</CodeBlock>

            <h3>First login &amp; API key</h3>
            <p>Open <code>http://localhost:21847/dashboard</code> and sign in with the bootstrap admin credentials from <code>.env</code> (<code>CIX_BOOTSTRAP_ADMIN_EMAIL</code> / <code>CIX_BOOTSTRAP_ADMIN_PASSWORD</code>). Go to <b>API&nbsp;Keys → New key</b>, name the key, and copy the revealed <code>cix_…</code> value — it is shown exactly once. The same dialog also gives you a ready-to-paste <code>cix config</code> connect command, so you can skip the manual configuration below.</p>
            <div className="shot-row">
              <figure className="shot shot-wide">
                <img src="/img/dashboard-api-keys.png" alt="The API keys page of the cix dashboard, with the New key button in the top right corner" loading="lazy" width="1600" height="1006" />
                <figcaption>API Keys → New key. Keys are bearer tokens for CLI / SDK access — created here, revoked here.</figcaption>
              </figure>
              <figure className="shot">
                <img src="/img/dashboard-create-key.png" alt="The Create API key dialog asking for a key name" loading="lazy" width="1024" height="502" />
                <figcaption>Name the key after the machine or agent that will use it.</figcaption>
              </figure>
              <figure className="shot">
                <img src="/img/dashboard-key-created.png" alt="The API key created dialog revealing the one-time key and a ready-to-paste cix config connect command" loading="lazy" width="1024" height="1094" />
                <figcaption>The full key is revealed once, together with a copy-paste connect command for the CLI. (The key on this screenshot is long revoked.)</figcaption>
              </figure>
            </div>

            <h3>CLI</h3>
            <CodeBlock>{`# One-line installer (macOS / Linux)
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash

# Paste the connect command from the API-key dialog, or:

# Interactive first-run wizard…
cix config init

# …or configure by hand (multi-server layout)
cix config set server.local.url http://localhost:21847
cix config set server.local.key cix_<your-token>   # from API Keys → New key
cix config set default_server local`}</CodeBlock>
          </Section>

          <Section id="cli" title="CLI reference" eyebrow="Commands">
            <h3>Project management</h3>
            <DefList rows={[
              ['cix init [path]',          'Register, index, and start the file watcher (disable with --watch=false).'],
              ['cix status',               'Show indexing status and progress.'],
              ['cix list',                 'List all indexed projects.'],
              ['cix reindex [--full]',     'Trigger a manual reindex (incremental by default).'],
              ['cix cancel',               'Cancel an in-flight indexing run.'],
              ['cix summary',              'Project overview: languages, directories, symbols.'],
              ['cix version',              'Print the CLI version.'],
            ]}/>

            <h3>Search</h3>
            <CodeBlock>{`# Semantic — natural language, finds by meaning
cix search <query> [--in <path>] [--exclude <path>] [--lang <lang>]
                   [--limit <n>] [--min-score <0-1>]

# Symbols — fast lookup by name
cix symbols <name> [--kind function|class|method|type] [--limit <n>]

# Definitions / references (aliases: def, goto · refs, usages)
cix def  <symbol> [--kind <kind>] [--file <path>] [--limit <n>]
cix refs <symbol> [--file <path>] [--limit <n>]

# Files by path pattern
cix files <pattern> [--limit <n>]

# Every command also takes -p <path> or -n <project-id>
# to target a project other than the current directory.`}</CodeBlock>

            <h3>External (GitHub-backed) projects</h3>
            <DefList rows={[
              ['cix file <path> [--lines N:M]', "Read a file (or a line range) from the project's server-side checkout."],
              ['cix tree [dir]',                'List one directory level of the server-side checkout.'],
            ]}/>

            <h3>Workspaces</h3>
            <CodeBlock>{`cix ws                                # list workspaces
cix ws create <name> [--description "…"]
cix ws <name>                         # describe: repos, sizes, sync state
cix ws <name> search "<query>" [--top-projects N] [--top-chunks K] [--json]
cix ws <name> add <project>…          # attach indexed projects
cix ws <name> remove <project>…
cix ws <name> rename <new-name>
cix ws <name> delete [-y]`}</CodeBlock>

            <h3>Agents (MCP)</h3>
            <DefList rows={[
              ['cix mcp',                          'Run cix as an MCP stdio server (launched by the host app, not by you).'],
              ['cix mcp install claude-desktop',   "Register cix in Claude Desktop's MCP config. Flags: --name, --print."],
              ['cix mcp uninstall claude-desktop', 'Remove the entry.'],
            ]}/>

            <h3>File watcher</h3>
            <DefList rows={[
              ['cix watch [path]',        'Start the background daemon.'],
              ['cix watch --foreground',  'Run in the terminal (Ctrl+C to stop).'],
              ['cix watch stop [--all]',  'Stop the daemon (or all daemons).'],
              ['cix watch status',        'Check if the daemon is running.'],
              ['cix watch list',          'List all running watcher daemons.'],
              ['cix watch manage',        'Interactive TUI to start/stop/restart watchers (alias: ui).'],
            ]}/>
            <p>The watcher uses native filesystem events (FSEvents on macOS, inotify on Linux) with a 5-second debounce. Logs live in <code>~/.cix/logs/</code>, one file per project (<code>watcher-&lt;hash&gt;.log</code>).</p>

            <h3>Configuration</h3>
            <DefList rows={[
              ['cix config show',              'Print the resolved config.'],
              ['cix config set <key> <value>', 'Set a value (see patterns below).'],
              ['cix config unset <key>',       'Remove a server or clear a key.'],
              ['cix config keys',              'List every settable key with defaults and env overrides.'],
              ['cix config path',              'Print the config file location.'],
              ['cix config edit',              'Interactive TUI editor.'],
              ['cix config init',              'First-run wizard.'],
            ]}/>
            <CodeBlock>{`# Multi-server layout
cix config set server.<name>.url  https://cix.example.com
cix config set server.<name>.key  cix_…
cix config set server.<name>.header.<Header>  <value>   # e.g. Cloudflare Access
cix config set default_server <name>`}</CodeBlock>
          </Section>

          <Section id="tuning" title="Tuning search" eyebrow="Quality">
            <p>The CLI defaults to <code>--min-score 0.4</code> (the raw HTTP endpoint defaults lower, 0.2). The threshold is calibrated for <b>CodeRankEmbed-Q8</b> with the path-aware embedding format. Score ranges look lower than generic models because CodeRankEmbed is asymmetric — queries get a different prefix than passages.</p>
            <table className="tbl">
              <thead><tr><th>Match strength</th><th>Score range</th><th>Action</th></tr></thead>
              <tbody>
                <tr><td>Exact symbol or filename</td><td><code>≥ 0.65</code></td><td>rare; very high confidence</td></tr>
                <tr><td>Strong path-aware concept</td><td><code>0.50 – 0.65</code></td><td>typical "good" match</td></tr>
                <tr><td>Weaker / partial overlap</td><td><code>0.40 – 0.50</code></td><td>typical for vague queries</td></tr>
                <tr><td>Likely unrelated noise</td><td><code>&lt; 0.40</code></td><td>filtered out by default</td></tr>
              </tbody>
            </table>
            <p><b>Lower the threshold</b> when the query returns no results but you know matching code exists (<code>--min-score 0.25</code>), or when exploring an unfamiliar codebase. <b>Raise it</b> when an agent's context is filling with weak matches.</p>
            <h3>Excluding noisy paths</h3>
            <CodeBlock>{`# One-off
cix search "main entry point" --exclude vendor --exclude bench/fixtures

# Permanent — add to .cixignore (skips indexing entirely)
echo "vendor/"             >> .cixignore
echo "bench/fixtures/"     >> .cixignore
cix reindex --full`}</CodeBlock>
            <p><code>.cixignore</code> follows <code>.gitignore</code> syntax exactly (it is read by the CLI's file discovery, alongside your <code>.gitignore</code>). A <code>.cixconfig.yaml</code> with <code>ignore:&nbsp;submodules:&nbsp;true</code> additionally excludes all git submodule paths.</p>
          </Section>

          <Section id="config" title="Configuration" eyebrow="Environment">
            <p>All server settings use the <code>CIX_*</code> prefix. This is the curated set — the full reference (~40 variables) lives in <a href={`${GITHUB_URL}/blob/main/doc/CONFIG_REFERENCE.md`} target="_blank" rel="noopener"><code>doc/CONFIG_REFERENCE.md</code></a>. Tuning values are also editable at runtime from <code>/dashboard/server</code>; env values are the boot-time seed.</p>
            <h3>Core</h3>
            <VarTable rows={[
              ['CIX_PORT', '21847', 'HTTP listen port.'],
              ['CIX_BOOTSTRAP_ADMIN_EMAIL', null, 'First-admin seed; only used while the users table is empty.'],
              ['CIX_BOOTSTRAP_ADMIN_PASSWORD', null, 'Forces a password change on first login.'],
              ['CIX_API_KEY', null, 'Optional; imported as a legacy API key at first boot.'],
              ['CIX_AUTH_DISABLED', 'false', 'Dev only. Never set in production.'],
              ['CIX_LOG_LEVEL', 'info', <><code>debug|info|warn|error</code>.</>],
            ]}/>
            <h3>Storage</h3>
            <VarTable rows={[
              ['CIX_DATA_DIR', '~/.cix/data', 'Root for everything below.'],
              ['CIX_SQLITE_PATH', '<data>/sqlite/projects.db', 'Model-independent system DB.'],
              ['CIX_CHROMA_PERSIST_DIR', '<data>/chroma', 'Vector store; namespaced per provider/model.'],
              ['CIX_REPOS_DIR', '<sqlite dir>/repos', 'Server-side clones of GitHub-backed projects.'],
            ]}/>
            <h3>Embeddings &amp; sidecar</h3>
            <VarTable rows={[
              ['CIX_EMBEDDING_MODEL', 'awhiteside/CodeRankEmbed-Q8_0-GGUF', <>Model for the local llama.cpp provider — HuggingFace repo or absolute .gguf path. Switch providers (Voyage, OpenAI) from the dashboard.</>],
              ['CIX_EMBEDDINGS_ENABLED', 'true', 'Disable to run symbol/file search only.'],
              ['CIX_MAX_EMBEDDING_CONCURRENCY', '5', 'Embedding queue parallelism.'],
              ['CIX_EMBED_INCLUDE_PATH', 'true', <>Path/lang/symbol preamble. Toggling needs <code>reindex --full</code>.</>],
              ['CIX_MAX_FILE_SIZE', '524288', 'Skip files larger than this (bytes).'],
              ['CIX_N_GPU_LAYERS', '-1 macOS · 0 else', <>-1 = all layers on Metal. The CUDA image bakes <code>99</code>.</>],
              ['CIX_LLAMA_TRANSPORT', 'unix', <>Sidecar IPC: <code>unix</code> or <code>tcp</code>.</>],
              ['CIX_LLAMA_STARTUP_TIMEOUT', '60', 'Seconds to wait for the sidecar.'],
            ]}/>
            <h3>GitHub &amp; tunnels</h3>
            <VarTable rows={[
              ['CIX_PUBLIC_URL', null, 'External origin for webhook URLs (a live tunnel takes precedence).'],
              ['CIX_TUNNEL_BIN_MANAGED', 'false', 'Let the server download/update tunnel binaries.'],
            ]}/>
          </Section>

          <Section id="api" title="REST API" eyebrow="HTTP">
            <p>Full machine-readable schema at <code>/openapi.json</code>; Swagger UI at <code>/docs</code>. Roughly 100 routes, grouped below. Each group is tagged with its access level — see <a href="#security">Security &amp; access</a> for what the levels mean.</p>

            <h3>Public — no auth</h3>
            <CodeBlock>{`GET  /health                          liveness
GET  /openapi.json · /docs            spec + Swagger UI
GET  /dashboard                       embedded web UI
GET  /api/v1/auth/bootstrap-status    first-run state
POST /api/v1/auth/login               email + password → cookie`}</CodeBlock>

            <h3>Auth &amp; account <Tag t="SelfAuth" /></h3>
            <CodeBlock>{`POST   /api/v1/auth/logout            clear cookie + server-side session
GET    /api/v1/auth/me                current user
POST   /api/v1/auth/change-password   forced or voluntary
GET    /api/v1/auth/sessions          list own sessions
DELETE /api/v1/auth/sessions/{id}     revoke a session
GET    /api/v1/api-keys               list own keys (admin sees all)
POST   /api/v1/api-keys               mint a new key
DELETE /api/v1/api-keys/{id}          revoke
GET    /api/v1/status                 live config snapshot
GET    /api/v1/jobs                   job queue`}</CodeBlock>

            <h3>Projects — indexing &amp; search <Tag t="Owner" /> <Tag t="GroupRead" /></h3>
            <CodeBlock>{`GET/POST          /api/v1/projects
GET/PATCH/DELETE  /api/v1/projects/{path}
POST /api/v1/projects/{path}/index/begin|files|finish|cancel
GET  /api/v1/projects/{path}/index/status
POST /api/v1/projects/{path}/search            semantic
POST /api/v1/projects/{path}/search/symbols|definitions|references|files
POST /api/v1/projects/{path}/file · /tree      external projects
GET  /api/v1/projects/{path}/summary · /workspaces`}</CodeBlock>
            <p>Lifecycle &amp; sharing routes use the project's <code>{'{hash}'}</code> (path hash) instead of the encoded path:</p>
            <CodeBlock>{`POST /api/v1/projects/{hash}/reindex · /force-stop
GET/PATCH /api/v1/projects/{hash}/git-repo     PUT …/git-repo/token
PUT  /api/v1/projects/{hash}/owner             reassign (admin)
GET/POST /api/v1/projects/{hash}/shares        DELETE …/shares/{groupId}
GET  /api/v1/projects/{hash}/webhook-info`}</CodeBlock>

            <h3>Workspaces <Tag t="Owner" /> <Tag t="GroupRead" /></h3>
            <CodeBlock>{`GET/POST         /api/v1/workspaces
GET/PATCH/DELETE /api/v1/workspaces/{id}
GET/POST         /api/v1/workspaces/{id}/projects
DELETE           /api/v1/workspaces/{id}/projects/{hash}
GET              /api/v1/workspaces/{id}/search      hybrid BM25 + dense
GET/POST         /api/v1/workspaces/{id}/shares      DELETE …/shares/{groupId}`}</CodeBlock>

            <h3>Webhooks <Tag t="HMAC" /></h3>
            <CodeBlock>{`POST /api/v1/webhooks/github/{hash}   per-repo HMAC-SHA256 (X-Hub-Signature-256)`}</CodeBlock>

            <h3>Admin <Tag t="Admin" /></h3>
            <CodeBlock>{`POST /api/v1/git-repos                       register a GitHub-backed project
GET/POST /api/v1/github-tokens               PUT/DELETE …/{id}
GET  /api/v1/github-tokens/{id}/accounts · /repos
GET  /api/v1/github/webhooks/origin          POST …/reconcile

GET/POST /api/v1/groups                      view-groups (sharing)
GET/PATCH/DELETE /api/v1/groups/{id}
GET/POST /api/v1/groups/{id}/members         DELETE …/members/{userId}

GET /api/v1/tunnels · /tunnels/status · /tunnels/binaries
GET/PUT /api/v1/tunnels/config               POST …/restart · /test
POST /api/v1/tunnels/binaries/{provider}/install|update

GET/POST /api/v1/admin/users                 PATCH/DELETE …/{id}
POST /api/v1/admin/users/{id}/reset-password
GET/PUT /api/v1/admin/runtime-config
GET /api/v1/admin/models · /login-locks      POST …/login-locks/reset
GET /api/v1/admin/embedding-providers        GET/PUT …/active · POST …/{kind}/test
POST /api/v1/admin/sidecar/restart           GET …/sidecar/status`}</CodeBlock>
          </Section>

          <Section id="workspaces" title="Workspaces" eyebrow="Multi-repo">
            <p>A <b>workspace</b> is a named group of repositories that cix searches as <b>one corpus</b>. Where <code>cix search</code> is for the project you're <code>cd</code>'d into, a workspace is for tasks that span repos — microservices that talk to each other, a feature whose implementation crosses several services, or any time the answer is "look in N repos, not one".</p>
            <p>Workspaces clone GitHub repositories server-side (under <code>CIX_REPOS_DIR</code>), index them next to your local projects, and expose a single <b>hybrid search</b> endpoint: a dense (cosine) pass and a BM25 (SQLite FTS5) pass fan out per project, then a two-stage ranker gates the relevant repos and fuses both signals. Defaults are calibrated on a 113-query eval. One query returns ranked projects <i>and</i> the top chunks across all of them — most of an agent's context needs land in a single round-trip.</p>
            <h3>Use it</h3>
            <p>Workspaces ship enabled in every release — no feature flag, nothing to turn on. Point clones at a dedicated volume with <code>CIX_REPOS_DIR</code> if you like, then:</p>
            <CodeBlock>{`cix ws create platform --description "Platform services"
cix ws platform add <project>…        # attach indexed projects
cix ws platform search "who validates webhook signatures"
cix ws platform search "feature flag rollout" --json   # for agents`}</CodeBlock>
            <p>GitHub-backed repositories are registered by an admin first (dashboard → workspace → <b>Add GitHub repository</b>, or <code>POST /api/v1/git-repos</code>) — with webhook or polling sync so the index follows <code>push</code>. See <a href="#github">GitHub repos &amp; tunnels</a>. Full guide: <a href={`${GITHUB_URL}/blob/main/workspaces.md`} target="_blank" rel="noopener"><code>workspaces.md</code></a>.</p>
          </Section>

          <Section id="mcp" title="MCP — Claude Desktop & Cowork" eyebrow="Agents">
            <p>Claude Desktop and Cowork don't load Claude Code plugins — the supported channel there is <b>MCP</b> (Model Context Protocol). So the CLI doubles as an MCP server: <code>cix mcp</code> speaks JSON-RPC over stdio and is launched by the host app, not by you.</p>
            <h3>One-command install</h3>
            <CodeBlock>{`cix mcp install claude-desktop     # writes the entry, keeps a .bak
# restart Claude Desktop — Cowork picks the tools up too

cix mcp install claude-desktop --print    # dry-run preview
cix mcp uninstall claude-desktop`}</CodeBlock>
            <p>No secrets are written to the host config — the server URL and API key stay in <code>~/.cix/config.yaml</code>.</p>
            <h3>The 13 tools</h3>
            <p><code>cix_search</code>, <code>cix_definitions</code>, <code>cix_references</code>, <code>cix_symbols</code>, <code>cix_files</code>, <code>cix_summary</code>, <code>cix_file</code>, <code>cix_tree</code>, <code>cix_workspace_search</code>, <code>cix_list_workspaces</code>, <code>cix_list_workspace_projects</code>, <code>cix_list_projects</code>, <code>cix_list_servers</code>.</p>
            <p>The model is deliberately <b>server-centric and multi-server</b>: there is no "current project" and nothing is inferred from a working directory. Every tool except <code>cix_list_servers</code> takes an optional <code>server</code> argument (an alias from your CLI config); the agent names the project or workspace explicitly. <code>cix_file</code>/<code>cix_tree</code> work for external (GitHub-backed) projects, which the server keeps on disk.</p>
            <h3>Optional Cowork skills</h3>
            <CodeBlock>{`/plugin install cix-cowork@code-index`}</CodeBlock>
            <p><code>cix-cowork</code> (v{COWORK_PLUGIN_VERSION}) is a skills-only companion plugin — no hooks, no CLI — that teaches Cowork the cix and cix-workspace research workflows over the MCP tools. The tools work without it.</p>
          </Section>

          <Section id="plugin" title="Claude Code plugin" eyebrow="Agents">
            <p>The official Claude Code plugin (v{PLUGIN_VERSION}) ships from the repo's marketplace. It bundles the CLI, <b>eight slash commands</b>, two lazy-loading skills (<code>cix</code> + <code>cix-workspace</code>), a <code>cix-workspace-investigator</code> sub-agent for parallel cross-repo research, and five behavioral hooks.</p>
            <h3>Install</h3>
            <CodeBlock>{`/plugin marketplace add dvcdsys/code-index
/plugin install cix@code-index`}</CodeBlock>
            <h3>What the hooks do</h3>
            <DefList rows={[
              ['SessionStart',            'Runs cix status (2s budget), caches the verdict for the session, and sweeps marker files older than 30 days.'],
              ['CwdChanged',              'Re-evaluates cix-awareness when Claude cd-s into another project.'],
              ['PostToolUse (Grep|Glob)',  'Reads the cache. Nudges toward cix with exponential backoff (call 1, 2, 4, 8, 16, 32…). Missing cache → silent.'],
              ['PostToolUse (Bash)',       'Same nudge when a Bash command is grep/rg/find-shaped.'],
              ['PostCompact',             'Re-injects the cix reminder after Claude Code compacts the conversation.'],
              ['SessionEnd',              'Best-effort cleanup of per-session marker files.'],
            ]}/>
            <p>Context cost stays small: the session reminder is a single paragraph, and the full <code>cix</code> SKILL.md (~13 KB) enters context at most once per session via native lazy-loading. Cross-repo work loads the <code>cix-workspace</code> skill on demand.</p>
          </Section>

          <Section id="github" title="GitHub repos & managed tunnels" eyebrow="Sync">
            <h3>External (GitHub-backed) projects</h3>
            <p>cix indexes two kinds of project. <b>Local projects</b> are created by the CLI (<code>cix init</code>) and are private to their owner. <b>External projects</b> are registered by an admin (<code>POST /api/v1/git-repos</code> or the dashboard): the server clones the GitHub repo under <code>CIX_REPOS_DIR</code>, indexes it, and keeps it in sync. Regular users see an external project only when an admin shares it into one of their <a href="#security">view-groups</a>.</p>
            <p>Sync happens per repo in one of three webhook modes:</p>
            <ul className="bullets">
              <li><b>auto</b> — the server registers the webhook on GitHub itself, using a stored personal access token (PAT needs <code>repo</code> + <code>admin:repo_hook</code>).</li>
              <li><b>manual</b> — you copy the URL + secret from <code>GET /api/v1/projects/{'{hash}'}/webhook-info</code> into GitHub's settings.</li>
              <li><b>disabled</b> — polling only (interval configurable).</li>
            </ul>
            <p>Deliveries hit <code>POST /api/v1/webhooks/github/{'{hash}'}</code> and are authenticated per repo with an HMAC-SHA256 signature over the body (<code>X-Hub-Signature-256</code>) — not with a cix API key. A valid push on the tracked branch enqueues a re-sync + reindex. PATs are stored encrypted and managed under <code>/api/v1/github-tokens</code> (admin-only; the plaintext is never echoed back).</p>
            <h3>Managed tunnels</h3>
            <p>Behind NAT but still want webhooks? The server can orchestrate an outbound tunnel itself (admin-only): <b>Cloudflare Tunnel</b> or <b>ngrok</b>. Configure under <b>Managed Tunnels</b> in the dashboard (or <code>GET/PUT /api/v1/tunnels/config</code>), optionally let the server download the provider binary (<code>CIX_TUNNEL_BIN_MANAGED=true</code>), then test and restart. Webhook URLs use the live tunnel origin when one is up, falling back to <code>CIX_PUBLIC_URL</code>; after enabling a tunnel, <code>POST /api/v1/github/webhooks/reconcile</code> re-registers hooks across all <i>auto</i> repos.</p>
          </Section>

          <Section id="security" title="Security & access model" eyebrow="Auth">
            <h3>Three authentication mechanisms</h3>
            <ul className="bullets">
              <li><b>Bearer API key</b> — <code>Authorization: Bearer cix_…</code> for the CLI, agents, and scripts.</li>
              <li><b>Session cookie</b> — <code>cix_session</code> (HttpOnly, SameSite=Strict) for the dashboard.</li>
              <li><b>Per-repo HMAC</b> — GitHub webhook deliveries are verified with HMAC-SHA256 against each repo's secret, independent of the other two.</li>
            </ul>
            <p>Public paths (no auth): <code>/health</code>, <code>/docs</code>, <code>/openapi.json</code>, <code>/dashboard</code> (the SPA shell), <code>/api/v1/auth/bootstrap-status</code>, <code>/api/v1/auth/login</code>, and the HMAC-guarded webhook receiver. Everything else requires an authenticated principal.</p>
            <h3>Roles &amp; authorization</h3>
            <p>Two roles: <b>admin</b> and <b>user</b>. On top of that, every resource is checked against an ownership + view-group model:</p>
            <table className="tbl">
              <thead><tr><th>Level</th><th>Who passes</th><th>Examples</th></tr></thead>
              <tbody>
                <tr><td><Tag t="Public" /></td><td>anyone</td><td>/health, /docs, login</td></tr>
                <tr><td><Tag t="HMAC" /></td><td>valid per-repo signature</td><td>GitHub webhooks</td></tr>
                <tr><td><Tag t="SelfAuth" /></td><td>any authenticated user (self-scoped)</td><td>own sessions, own API keys</td></tr>
                <tr><td><Tag t="Owner" /></td><td>resource owner or admin</td><td>project settings, workspace edit</td></tr>
                <tr><td><Tag t="GroupRead" /></td><td>owner, view-group member, or admin</td><td>read/search shared projects</td></tr>
                <tr><td><Tag t="Admin" /></td><td>admins only</td><td>git-repos, PATs, groups, tunnels, users</td></tr>
              </tbody>
            </table>
            <p>Local projects are private to their creator. External (GitHub-backed) projects are ownerless and admin-administered — they reach users only through <b>view-groups</b> (read/search). Reads on resources you can't see return 404, not 403, so existence isn't leaked. Workspace search results are automatically intersected with the projects you're allowed to see.</p>
          </Section>

          <Section id="troubleshoot" title="Troubleshooting" eyebrow="When it breaks">
            <DefList rows={[
              ['Server refuses to start', <>Set both <code>CIX_BOOTSTRAP_ADMIN_EMAIL</code> and <code>CIX_BOOTSTRAP_ADMIN_PASSWORD</code> in <code>.env</code>. They're only used while the users table is empty — drop them after first login.</>],
              ['CLI can’t authenticate', <>Run <code>cix config set server.&lt;name&gt;.key cix_…</code> with a key minted from the dashboard (or run <code>cix config init</code>). Check what's active with <code>cix config show</code>.</>],
              ['"connection refused"', <>Server isn't running. <code>curl http://localhost:21847/health</code>; if it fails, <code>docker compose up -d</code>.</>],
              ['Search returns no results', <>Lower the threshold (<code>--min-score 0.2</code>), verify <code>cix status</code> says Indexed, and confirm <code>cix list</code> shows the project.</>],
              ['"Stale model" on every project', <>The embedding model changed. Reindex each project (after a model change the server automatically promotes it to a full reindex) — or revert the change in <b>Server → Embedding provider</b>.</>],
              ['Workspace endpoints return 503', <>Workspaces ship in every release (no feature flag), so a 503 means the workspaces service failed to wire — usually the encryption layer for GitHub tokens didn't initialize. Check the encryption-key config (<a href={`${GITHUB_URL}/blob/main/doc/WORKSPACES.md`} target="_blank" rel="noopener"><code>doc/WORKSPACES.md</code></a>); a plain restart won't help.</>],
              ['Locked out of the admin account', <>Another admin can reset you via <b>Users → Reset password</b> (or <code>POST /api/v1/admin/users/{'{id}'}/reset-password</code>) and clear login locks under <b>Login locks</b>. Long-term: keep two admin accounts.</>],
            ]}/>
          </Section>

          <div style={{ marginTop: 80, padding: 24, background: 'var(--bg-2)', borderRadius: 18, border: '2px solid var(--ink)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 14 }}>
            <div>
              <b>Something missing or wrong?</b>{' '}
              <span style={{ color: 'var(--ink-soft)' }}>The full docs live in the repo — open a PR.</span>
            </div>
            <a className="btn btn-primary" href={`${GITHUB_URL}/tree/main/doc`} target="_blank" rel="noopener">Docs on GitHub →</a>
          </div>
        </article>
      </main>

      <Foot slim homeHref="/" />
    </>
  );
}

function Section({ id, title, eyebrow, badge, children }) {
  return (
    <section id={id} className="docs-section">
      <span className="eyebrow">{eyebrow}</span>
      <h2>
        {title}
        {badge && <span className="sticker" style={{ marginLeft: 12, background: 'var(--ochre)', fontSize: 12, verticalAlign: 'middle' }}>{badge}</span>}
      </h2>
      {children}
    </section>
  );
}

function Tag({ t }) {
  const colors = {
    Public: 'var(--moss)', HMAC: 'var(--plum)', SelfAuth: 'var(--ochre)',
    Owner: 'var(--red)', GroupRead: 'var(--red-deep)', Admin: 'var(--ink)',
  };
  return (
    <span style={{
      display: 'inline-block', fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700,
      color: 'var(--paper)', background: colors[t] || 'var(--ink)', borderRadius: 6,
      padding: '1px 7px', letterSpacing: '0.04em', verticalAlign: 'middle',
    }}>{t}</span>
  );
}

function CodeBlock({ children }) {
  return <pre className="code-pre">{children}</pre>;
}

// Environment-variable table: rows are [name, default (null → —), notes].
function VarTable({ rows }) {
  return (
    <table className="tbl">
      <thead><tr><th>Variable</th><th>Default</th><th>Notes</th></tr></thead>
      <tbody>
        {rows.map(([name, def, notes]) => (
          <tr key={name}>
            <td><code>{name}</code></td>
            <td>{def == null ? '—' : <code>{def}</code>}</td>
            <td>{notes}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DefList({ rows }) {
  return (
    <dl className="def-list">
      {rows.map(([k, v], i) => (
        <div key={i} className="def-row">
          <dt><code>{k}</code></dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Diagram() {
  return (
    <div className="diagram card card-flat" style={{ marginTop: 18 }}>
      <pre className="ascii-diagram">{`           Browser  →  http://host:21847
                          │
                          ▼
   ┌────────────────────────────────────────────────────┐
   │  cix-server  (Go, distroless)                      │
   │  ─────────────────────────────────────────────────│
   │  HTTP/REST · sessions + Bearer keys · /dashboard   │
   │  ├── tree-sitter chunker   (31 languages, WASM)    │
   │  ├── llama-server sidecar  (Unix socket)           │
   │  ├── chromem-go            (cosine vector store)   │
   │  ├── SQLite FTS5 mirror    (BM25 → hybrid rank)    │
   │  └── modernc/sqlite        (symbols + metadata)    │
   └─────┬─────────────────────────────────────┬────────┘
         │ HTTP                                │ Unix sock
         ▼                                     ▼
    cix CLI (Go)                  llama-server child proc
    search · symbols · refs       CodeRankEmbed Q8 GGUF`}</pre>
    </div>
  );
}
