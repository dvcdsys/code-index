import { CixAscii } from './ascii.jsx';
import { SERVER_VERSION, CLI_VERSION, GITHUB_URL, DOCKER_HUB_URL } from './versions.js';

export function Foot({ slim, homeHref = '/' }) {
  return (
    <footer className="foot" style={slim ? { marginTop: 80 } : undefined}>
      <div className="wrap">
        <div className="foot-inner">
          <div>
            <a className="nav-logo" href={homeHref} style={{ marginBottom: 12 }} aria-label="cix — CodeIndeX, home">
              <CixAscii style={{ fontSize: 9 }} withTag />
            </a>
            {slim ? (
              <p style={{ color: 'var(--ink-soft)', fontSize: 14, marginTop: 8 }}>MIT · self-hosted · no telemetry</p>
            ) : (
              <p style={{ maxWidth: 360, color: 'var(--ink-soft)', fontSize: 15, marginTop: 8 }}>
                cix (CodeIndeX) — a self-hosted semantic index for your code. Built by <a href="https://github.com/dvcdsys" target="_blank" rel="noopener">@dvcdsys</a>. MIT-licensed, no telemetry, runs on your machine.
              </p>
            )}
          </div>
          <div className="foot-cols">
            <div className="foot-col">
              <h4>Project</h4>
              <a href={GITHUB_URL} target="_blank" rel="noopener">GitHub</a>
              <a href={DOCKER_HUB_URL} target="_blank" rel="noopener">Docker Hub</a>
              <a href={`${GITHUB_URL}/releases`} target="_blank" rel="noopener">Releases</a>
              <a href={`${GITHUB_URL}/blob/main/LICENSE`} target="_blank" rel="noopener">MIT License</a>
            </div>
            <div className="foot-col">
              <h4>Docs</h4>
              <a href="/docs/">Overview</a>
              <a href="/docs/#api">REST API</a>
              <a href="/docs/#workspaces">Workspaces</a>
              <a href="/docs/#troubleshoot">Troubleshooting</a>
            </div>
            <div className="foot-col">
              <h4>Community</h4>
              <a href={`${GITHUB_URL}/issues`} target="_blank" rel="noopener">Issues</a>
              <a href={`${GITHUB_URL}/blob/main/CONTRIBUTING.md`} target="_blank" rel="noopener">Contributing</a>
              <a href={`${GITHUB_URL}/blob/main/SECURITY.md`} target="_blank" rel="noopener">Security policy</a>
            </div>
          </div>
        </div>
        <div className="foot-bottom">
          <span>© 2026 cix · MIT · server v{SERVER_VERSION} · CLI v{CLI_VERSION}</span>
          <span>made with grep, then with cix</span>
        </div>
      </div>
    </footer>
  );
}
