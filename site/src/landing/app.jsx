import { CixAscii } from '../shared/ascii.jsx';
import { Foot } from '../shared/foot.jsx';
import { SERVER_VERSION, CLI_VERSION, GITHUB_URL } from '../shared/versions.js';
import { HeroTerminal } from './terminal.jsx';
import { Fit, Why, Features, Playground, Workspaces, Agent, CaseStudy, QuickStart, FAQ } from './sections.jsx';

export function App() {
  return (
    <>
      <Nav />
      <main>
        <Hero />
        <div className="zigzag" />
        <Fit />
        <Why />
        <Features />
        <Playground />
        <Workspaces />
        <Agent />
        <CaseStudy />
        <QuickStart />
        <FAQ />
      </main>
      <Foot homeHref="#top" />
    </>
  );
}

function Nav() {
  return (
    <header className="nav">
      <div className="wrap nav-inner">
        <a className="nav-logo" href="#top" aria-label="cix — CodeIndeX, home">
          <CixAscii withTag />
        </a>
        <nav className="nav-links">
          <a className="nav-sec" href="#why">Why</a>
          <a className="nav-sec" href="#features">Features</a>
          <a className="nav-sec" href="#workspaces">Workspaces</a>
          <a className="nav-sec" href="#agent">For agents</a>
          <a className="nav-sec" href="#quickstart">Quick start</a>
          <a href="/docs/">Docs</a>
          <a href={GITHUB_URL} target="_blank" rel="noopener" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span>GitHub</span>
            <span aria-hidden style={{ fontSize: 16 }}>↗</span>
          </a>
        </nav>
      </div>
    </header>
  );
}

function Hero() {
  return (
    <section className="wrap hero" id="top">
      <div className="hero-copy" style={{ position: 'relative' }}>
        <span className="stamp hero-stamp" tabIndex={0} aria-label="vibecoded for vibecoding">
          <span className="stamp-line-1">vibecoded</span>
          <span className="stamp-rule" />
          <span className="stamp-line-2">for vibecoding</span>
          <span className="stamp-tip" role="tooltip">
            <strong>heads up</strong>
            Large parts of this project are agent-generated. Use at your own risk — though I do, every day. PRs and issues welcome.
          </span>
        </span>
        <span className="eyebrow">server v{SERVER_VERSION} + CLI v{CLI_VERSION} + skills · MIT · <span style={{ whiteSpace: 'nowrap' }}>self-hosted</span></span>
        <h1 style={{ marginTop: 18 }}>
          Search your codebase by{' '}
          <span className="blob">meaning</span>,<br />
          not just text.
        </h1>
        <p className="lead" style={{ marginTop: 20 }}>
          <code style={{ fontFamily: 'var(--font-mono)', fontWeight: 700, color: 'var(--red)', fontSize: '1.05em' }}>cix</code> (CodeIndeX) is a self-hosted platform for codebase indexing and semantic code search — a server with an embedded dashboard, a thin CLI for terminals and agents, and skills that teach Claude to reach for it. Hybrid BM25 + dense ranking, <code>file:line</code> snippets in milliseconds. Deploy once — a single Go binary or Docker — and point every repo, teammate and agent at it.
        </p>
        <div className="hero-cta">
          <a className="btn btn-primary" href="#quickstart">Get started →</a>
          <a className="btn btn-ochre" href={GITHUB_URL} target="_blank" rel="noopener">★ Star on GitHub</a>
        </div>
        <div className="hero-meta">
          <span><b>31</b> languages via tree-sitter</span>
          <span><b>768d</b> CodeRankEmbed Q8</span>
          <span><b>BM25 + dense</b> hybrid ranking</span>
          <span><b>multi-user</b> self-hosted platform</span>
        </div>
      </div>
      <div style={{ position: 'relative' }}>
        <HeroTerminal />
        <span className="sticker" style={{ position: 'absolute', bottom: -14, right: 18, background: 'var(--ochre)', zIndex: 5, boxShadow: '3px 3px 0 var(--ink)', padding: '4px 12px', margin: 0, letterSpacing: '0.04em' }}>
          real output · cix on its own repo
        </span>
      </div>
    </section>
  );
}
