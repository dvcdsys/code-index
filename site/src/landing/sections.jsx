import { CLITabs } from './tabs.jsx';
import { WorkspaceDemo } from './workspace-demo.jsx';
import { GITHUB_URL, PLUGIN_VERSION } from '../shared/versions.js';

export function Why() {
  return (
    <section className="section" id="why">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">The problem</span>
          <h2>Grep doesn't scale to the way<br/>you actually search code.</h2>
          <p className="lead">A semantic index turns vague intent into ranked snippets — so agents stop burning context on file dumps, and you stop guessing what something is called.</p>
        </div>
        <div className="why-grid">
          <div className="card why-card">
            <span className="num">01</span>
            <h3>You have to know the name.</h3>
            <p>Grep only finds what you already remember. New codebase, new vocabulary, new dead-end.</p>
          </div>
          <div className="card why-card">
            <span className="num">02</span>
            <h3>Results flood with noise.</h3>
            <p>Vendored deps, generated files, unrelated tests. The signal you wanted gets buried on page two of three hundred.</p>
          </div>
          <div className="card why-card">
            <span className="num">03</span>
            <h3>Agents waste tokens.</h3>
            <p>Every Grep round-trip drags whole files into context. By task three, the model is reading more than it's writing.</p>
          </div>
        </div>
      </div>
    </section>
  );
}

const FEATURES = [
  { glyph: 'S', cls: 'alt', title: 'Semantic search by meaning',
    body: 'Tree-sitter chunks every function, class and method — 31 languages, parsed in a WASM sandbox. CodeRankEmbed Q8 embeds them with a path-aware preamble. Ranked in milliseconds.' },
  { glyph: '⌘', cls: '', title: 'Symbols, defs & refs',
    body: 'Five search modes share one engine: semantic, symbols, definitions, references, files. Same fidelity in CLI, dashboard and agent.' },
  { glyph: '⊞', cls: 'plum', title: 'Workspaces: search N repos as one',
    body: 'Group repositories into a named workspace and search the union — hybrid BM25 + dense ranking surfaces the right repo first. One query returns context from every repo at once.' },
  { glyph: '~', cls: 'moss', title: 'Live file watcher',
    body: 'Native filesystem events (FSEvents / inotify) with a 5-second debounce. Edit a file — the index follows. SHA-256 hashes mean only changed files re-embed.' },
  { glyph: '⊕', cls: '', title: 'Embedded dashboard',
    body: 'React SPA baked into the Go binary at /dashboard. Projects, search, users, API keys, runtime sidecar control. No extra service.' },
  { glyph: '⏻', cls: 'alt', title: 'Self-hosted. GPU optional.',
    body: 'Single distroless container; your code stays on your network by default. CUDA image for NVIDIA, native Metal on Apple Silicon — or plain CPU: the model is 145 MB on disk, ~0.7 GB VRAM.' },
];

export function Features() {
  return (
    <section className="section section-alt" id="features">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">What you get</span>
          <h2>One binary. Five search modes.<br/>Every agent speaks it.</h2>
        </div>
        <div className="feat-grid">
          {FEATURES.map((f, i) => (
            <div key={i} className={`card feat ${f.cls}`}>
              <div className="glyph">{f.glyph}</div>
              <h3>{f.title}</h3>
              <p>{f.body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

export function Playground() {
  return (
    <section className="section" id="playground">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">CLI tour</span>
          <h2>Six commands. One indexed brain.</h2>
          <p className="lead">Click through — the same text an agent gets back.</p>
        </div>
        <CLITabs />
      </div>
    </section>
  );
}

export function Workspaces() {
  return (
    <section className="section" id="workspaces">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">Multi-repo</span>
          <h2>One question. All your repos.<br/>One concrete answer.</h2>
          <p className="lead">A <b>workspace</b> groups any number of repositories into one searchable corpus. The agent starts broad — hybrid BM25 + dense ranking surfaces the services involved — then drills into each repo with targeted lookups, and comes back with changes you can actually make.</p>
        </div>
        <WorkspaceDemo />
        <p className="ws-note">
          Example scenario · <a href="/docs/#workspaces">workspace docs →</a>
        </p>
      </div>
    </section>
  );
}

export function Agent() {
  return (
    <section className="section section-alt" id="agent">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">Built for agents</span>
          <h2>The IDE your AI agent didn't have.</h2>
          <p className="lead">Most agents grep their way through unfamiliar code, file by file. cix gives them <i>goto-definition</i>, <i>find-references</i> and ranked semantic search — as a single shell tool, with a Claude Code plugin that ships the CLI and nudges Claude to reach for it.</p>
        </div>
        <div className="agent">
          <div className="card agent-card">
            <span className="sticker">Claude Code plugin · v{PLUGIN_VERSION}</span>
            <h3 style={{ marginTop: 14 }}>Two commands. Hooks register themselves.</h3>
            <p>The plugin ships the CLI, eight slash commands, two lazy-loading skills, a workspace-research sub-agent, and five behavioral hooks. SessionStart checks if your project is indexed; a PostToolUse hook nudges Claude — with exponential backoff — to reach for cix instead of Grep.</p>
            <pre className="code-block" style={{ margin: '12px 0' }}>
              <span className="prompt">&gt;</span> /plugin marketplace add dvcdsys/code-index{'\n'}
              <span className="prompt">&gt;</span> /plugin install cix@code-index
            </pre>
            <ul className="cmd-list">
              <li><b>/cix:search</b><span>natural-language semantic search</span></li>
              <li><b>/cix:def</b><span>jump to definition</span></li>
              <li><b>/cix:refs</b><span>find every callsite</span></li>
              <li><b>/cix:init</b><span>register + index + watch</span></li>
              <li><b>/cix:status</b><span>indexing state, drift, queue</span></li>
              <li><b>/cix:summary</b><span>language + symbol overview</span></li>
              <li><b>/cix:file</b><span>read a file from an external repo</span></li>
              <li><b>/cix:tree</b><span>browse an external repo's tree</span></li>
            </ul>
          </div>
          <div className="card flow-card">
            <span className="sticker" style={{ background: 'var(--ochre)' }}>How a session feels</span>
            <div className="flow-step">
              <span className="n">1</span>
              <div className="body"><b>SessionStart fires</b>silently runs <code>cix status</code> with a 2-second budget. Verdict cached for the session; stale markers older than 30 days get swept.</div>
            </div>
            <div className="flow-step">
              <span className="n">2</span>
              <div className="body"><b>Claude reaches for Grep</b>a PostToolUse hook reads the cache. If the project is indexed, it slips in a one-line nudge: <em>“try cix search instead.”</em> Grep-shaped Bash commands count too.</div>
            </div>
            <div className="flow-step">
              <span className="n">3</span>
              <div className="body"><b>Backoff kicks in</b>fires on call 1, 2, 4, 8, 16, 32… — never spammy. Each project tracks its own counter.</div>
            </div>
            <div className="flow-step">
              <span className="n">4</span>
              <div className="body"><b>SKILL.md lazy-loads</b>the full reference (~13 KB) enters context only when invoked, once per session. Cross-repo work pulls in the workspace skill on demand.</div>
            </div>
            <div className="flow-step">
              <span className="n">5</span>
              <div className="body"><b>SessionEnd cleans up</b>per-session markers are deleted best-effort; the SessionStart sweep catches anything a killed session left behind.</div>
            </div>
          </div>
        </div>
        <div className="card" style={{ marginTop: 'var(--gap)', padding: 'var(--pad-card)' }}>
          <span className="sticker" style={{ background: 'var(--moss)', color: 'var(--paper)' }}>Claude Desktop &amp; Cowork · MCP</span>
          <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap', alignItems: 'flex-start', marginTop: 14 }}>
            <div style={{ flex: '1 1 340px' }}>
              <h3>No plugin support? Speak MCP.</h3>
              <p>Claude Desktop and Cowork don't load Claude Code plugins — so cix ships a built-in MCP server instead. <code>cix mcp</code> exposes 13 tools (search, definitions, references, symbols, files, workspaces and more) over stdio, talks to any number of cix servers, and registers itself in one command.</p>
            </div>
            <pre className="code-block" style={{ flex: '1 1 300px', margin: 0 }}>
              <span className="prompt">$</span> cix mcp install claude-desktop{'\n'}
              <span className="comment"># restart Claude Desktop — done.</span>{'\n'}
              <span className="prompt">&gt;</span> /plugin install cix-cowork@code-index{'\n'}
              <span className="comment"># optional Cowork skills</span>
            </pre>
          </div>
        </div>
      </div>
    </section>
  );
}

const QS_STEPS = [
  {
    n: '01', title: 'Run the server',
    desc: <>Docker Compose for CPU. <code>docker-compose.cuda.yml</code> for NVIDIA (driver ≥ 525 + Container Toolkit). On Apple Silicon, run natively for Metal: <code>cd server && make bundle && make run</code>.</>,
    code: <>
      <span className="prompt">$</span> git clone https://github.com/dvcdsys/code-index{'\n'}
      <span className="prompt">$</span> cd code-index && cp .env.example .env{'\n'}
      <span className="comment"># set CIX_BOOTSTRAP_ADMIN_EMAIL + _PASSWORD</span>{'\n'}
      <span className="prompt">$</span> docker compose up -d
    </>,
  },
  {
    n: '02', title: 'Log in & mint an API key',
    desc: <>Sign in with the bootstrap admin from <code>.env</code>, then <b>API&nbsp;Keys → Create key</b>. The key is revealed exactly once — and the dialog hands you a ready-to-paste <code>cix config</code> connect command for the next step.</>,
    code: <>
      <span className="prompt">$</span> open http://localhost:21847/dashboard{'\n'}
      <span className="comment"># sign in with the bootstrap admin from .env</span>{'\n'}
      <span className="comment"># API Keys → Create key → copy cix_…</span>{'\n'}
      <span className="comment"># (shown once, with a ready connect command)</span>
    </>,
  },
  {
    n: '03', title: 'Install the CLI',
    desc: <>One-liner for macOS and Linux. Paste the connect command from the key dialog, run the interactive <code>cix config init</code> wizard, or set the server by hand with <code>cix config set</code>.</>,
    code: <>
      <span className="prompt">$</span> curl -fsSL https://raw.githubusercontent.com{'\n'}
      {'    '}/dvcdsys/code-index/main/install.sh | bash{'\n'}
      <span className="prompt">$</span> cix config set server.local.url http://localhost:21847{'\n'}
      <span className="prompt">$</span> cix config set server.local.key cix_xxx
    </>,
  },
  {
    n: '04', title: 'Index & first search',
    desc: <><code>cix init</code> registers, indexes, and starts the file watcher in the background. Search from the terminal, the dashboard, or any agent with shell access.</>,
    code: <>
      <span className="prompt">$</span> cd ~/code/your-project{'\n'}
      <span className="prompt">$</span> cix init{'\n'}
      <span className="comment"># → registered. indexing 2,431 files…</span>{'\n'}
      <span className="prompt">$</span> cix search "rate limit per IP"{'\n'}
      <span className="prompt">$</span> cix refs RateLimiter
    </>,
  },
  {
    n: '05', title: 'Hook up your agent',
    desc: <>The plugin ships the CLI, eight slash commands, the <code>/cix</code> and <code>/cix-workspace</code> skills, and hooks that steer Claude toward cix in indexed projects. Claude Desktop / Cowork: <code>cix mcp install claude-desktop</code>.</>,
    code: <>
      <span className="prompt">&gt;</span> /plugin marketplace add dvcdsys/code-index{'\n'}
      <span className="prompt">&gt;</span> /plugin install cix@code-index{'\n'}
      <span className="comment"># /cix:search /cix:def /cix:refs /cix:init</span>{'\n'}
      <span className="comment"># /cix:status /cix:summary /cix:file /cix:tree</span>{'\n'}
      <span className="comment"># skills: /cix · /cix-workspace</span>
    </>,
  },
];

export function QuickStart() {
  return (
    <section className="section" id="quickstart">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">Quick start</span>
          <h2>Clone to agent-ready.<br/>About ten minutes.</h2>
        </div>
        <div className="qs-grid">
          {QS_STEPS.map(s => (
            <div key={s.n} className="card qs-step">
              <div className="head">
                <div className="n">{s.n}</div>
                <h3>{s.title}</h3>
              </div>
              <pre className="code-block">{s.code}</pre>
              <p>{s.desc}</p>
            </div>
          ))}
        </div>
        <div style={{ marginTop: 36, display: 'flex', gap: 14, flexWrap: 'wrap', alignItems: 'center' }}>
          <a className="btn btn-primary" href={GITHUB_URL} target="_blank" rel="noopener">★ Star on GitHub</a>
          <a className="btn" href="/docs/">Read the docs →</a>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: 'var(--ink-mute)' }}>MIT · self-hosted · no telemetry</span>
        </div>
      </div>
    </section>
  );
}

const FAQS = [
  { q: 'Does my code leave my machine?',
    a: <>Not by default. The server runs on your hardware (Docker, native macOS, or your own GPU box), and embeddings happen locally via a llama.cpp sidecar — no SaaS endpoint, no telemetry. If you <i>choose</i> to switch the embedding provider to a remote API (Voyage, OpenAI-compatible), chunks go to that provider; that's an explicit admin action, off by default.</> },
  { q: "How is this different from Sourcegraph, GitHub code search, or Cursor's indexing?",
    a: <>Scope. Those give you search inside <i>their</i> surface — a web app, a code host, one editor. cix ships the entire path as one MIT repo you run yourself: the Go server with an embedded dashboard, the CLI, the file watcher, multi-repo workspaces, a Claude Code plugin (slash commands, skills, hooks), an MCP server for Claude Desktop &amp; Cowork, and team-deployment docs down to TLS, backups and upgrades. It's not an indexer you build a workflow around — it <i>is</i> the workflow, from <code>docker compose up</code> to your agent quoting <code>file:line</code>.</> },
  { q: 'Why a custom embedding model? Can I use OpenAI?',
    a: <>The default is <code>CodeRankEmbed</code> — a model purpose-built for code retrieval. It's asymmetric: queries get a different prefix than passages, so cosine scores look lower than generic models (a strong match here is ~0.55, not 0.80). You can swap in any GGUF from HuggingFace via <code>CIX_EMBEDDING_MODEL</code> (PyTorch repos aren't supported — inference goes through the llama-server sidecar), or configure a remote provider from the dashboard.</> },
  { q: 'What languages are supported?',
    a: <>31 tree-sitter grammars: Python, TypeScript + TSX, JavaScript, Go, Rust, Java, C, C++, C#, Ruby, PHP, Swift, Kotlin, Scala, Bash, Lua, Dart, R, Objective-C, HTML, CSS, SCSS, SQL, Markdown, Zig, Julia, Fortran, Haskell, OCaml, and Solidity. Anything else falls back to a sliding-window chunker, so no file is silently skipped.</> },
  { q: 'How does the agent know to use cix instead of grep?',
    a: <>Two ways. Drop a <code>cix</code> section in <code>~/.claude/CLAUDE.md</code> and any agent will prefer the CLI. For Claude Code specifically, install the plugin — a PostToolUse hook nudges with exponential backoff (call 1, 2, 4, 8, 16…) only in indexed projects, and stays silent otherwise. The full SKILL.md lazy-loads on demand.</> },
  { q: 'What happens when I change the embedding model?',
    a: <>Nothing is destroyed. The system database is model-independent; only the vector store is namespaced — each provider/model gets its own directory, so old and new vectors never mix. Every project remembers which model it was indexed with, the dashboard shows a "stale model" badge on drifted projects, and the next reindex (automatically promoted to a full one after a model change) clears it.</> },
  { q: "What's a workspace?",
    a: <>A named group of repositories that cix searches as one corpus — for microservices, cross-repo features, or "the answer is in one of these five repos" tasks. Repos are cloned server-side, indexed next to your local projects, and queried through a single hybrid BM25 + dense endpoint. Enabled in every release — no flag to flip.</> },
  { q: 'Why port 21847?',
    a: <>Because <code>21847</code> is unassigned by IANA and doesn't collide with anything common. Override with <code>CIX_PORT</code>.</> },
];

export function FAQ() {
  return (
    <section className="section" id="faq">
      <div className="wrap">
        <div className="section-head">
          <span className="eyebrow">Questions</span>
          <h2>Things people ask<br/>before they install.</h2>
        </div>
        <div className="faq-list">
          {FAQS.map((f, i) => (
            <details key={i} className="faq-item" open={i === 0}>
              <summary className="faq-q">
                {f.q}
                <span className="plus">+</span>
              </summary>
              <div className="faq-a">{f.a}</div>
            </details>
          ))}
        </div>
      </div>
    </section>
  );
}
