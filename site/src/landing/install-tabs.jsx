import { useState } from 'react';
import { MacPanel } from './mac-panel.jsx';
import { useMacRelease, formatSize } from '../shared/mac-release.js';
import { GITHUB_URL, SERVER_VERSION } from '../shared/versions.js';

// Quick start, split by how you actually install: the Mac app, Docker, or a
// checkout. The three used to be one prose paragraph inside step 01, which
// made the Mac app — the recommended route on the platform most visitors
// arrive from — a parenthetical inside a curl command meant for Linux.
//
// Every command here is transcribed from the repo: install-server.sh's own
// modes, README's manual-Docker block, doc/MACOS_APP.md and
// doc/SETUP_MACOS_NATIVE.md. Keep it that way — see site/README.md rule 1.

const RAW = 'https://raw.githubusercontent.com/dvcdsys/code-index/main';

function Steps({ steps }) {
  return (
    <ol className="inst-steps">
      {steps.map((s, i) => (
        <li className="inst-step" key={s.title}>
          <span className="n">{String(i + 1).padStart(2, '0')}</span>
          <div className="inst-step-body">
            <h4>{s.title}</h4>
            {s.code && <pre className="code-block">{s.code}</pre>}
            <p>{s.desc}</p>
          </div>
        </li>
      ))}
    </ol>
  );
}

function MacPane() {
  const rel = useMacRelease();
  const size = formatSize(rel.size);

  return (
    <div className="inst-cols">
      <div className="inst-aside">
        <MacPanel />
      </div>

      <div className="inst-main">
        <div className="inst-dl">
          <a className="btn btn-primary" href={rel.url}>↓ Download cix.app {rel.version}</a>
          <span className="inst-dl-meta">
            Apple Silicon · macOS 13+{size ? ` · ${size}` : ' · ~4 MB'} ·{' '}
            <a href={rel.page} target="_blank" rel="noopener">release notes &amp; checksums ↗</a>
          </span>
        </div>

        <Steps steps={[
          {
            title: 'Drag it to Applications',
            desc: <>Open the disk image and drag <b>cix.app</b> onto <b>Applications</b> — then launch it from there, not from the mounted image. A quarantined app opened anywhere else runs from a randomised read-only copy (App Translocation) that breaks the moment macOS discards it; the launcher detects that and asks you to move it.</>,
          },
          {
            title: 'Clear the download flag first',
            code: <>
              <span className="prompt">$</span> xattr -d com.apple.quarantine \{'\n'}
              {'    '}~/Downloads/cix-*-arm64.dmg{'\n'}
              <span className="comment"># run it BEFORE opening the image —</span>{'\n'}
              <span className="comment"># the app inherits the flag when you drag it out</span>
            </>,
            desc: <>cix is signed <b>ad-hoc</b>, not with a paid Apple Developer certificate, so macOS blocks it twice — once for the image, once for the app — saying it "could not verify" them. Nothing is wrong with the download. To click through instead: choose <b>Done</b> (never <i>Move to Bin</i>), then <b>System&nbsp;Settings → Privacy&nbsp;&amp;&nbsp;Security → Open Anyway</b>, twice.</>,
          },
          {
            title: 'First launch sets everything up',
            desc: <>Exactly what the panel on the left is playing: one question — an email address for the admin account, which goes nowhere — then it downloads the server, the CLI and a Metal-accelerated <code>llama-server</code> (about 40&nbsp;MB) into <code>~/.cix/runtime/</code>, generates your password and an API key, starts the server and hands you the login. You change that password on first sign-in.</>,
          },
        ]} />

        <p className="inst-note">
          <b>That is the install.</b> No terminal, no toolchain, nothing left to configure — the server is running, your account exists, and the app keeps both halves current on their own release streams (a server update is a download, a signature check and a symlink rename, with automatic rollback if the new one does not come back). The <code>cix</code> command is a separate install, identical on every platform: it is the first of the shared steps below.
        </p>
      </div>
    </div>
  );
}

function DockerPane() {
  return (
    <div className="inst-cols">
      <div className="inst-aside">
        <div className="inst-facts">
          <h4>Two images, never merged</h4>
          <dl>
            <dt><code>dvcdsys/code-index:latest</code></dt>
            <dd>CPU · distroless · ~80&nbsp;MB · any OS with Docker</dd>
            <dt><code>dvcdsys/code-index:cu128</code></dt>
            <dd>NVIDIA CUDA 12.x · ~1.1&nbsp;GB · driver ≥&nbsp;525 + Container&nbsp;Toolkit</dd>
          </dl>
          <p>Both run as a non-root user with no shell, and the healthcheck is the binary itself — no <code>curl</code> in the runtime.</p>
          <p className="inst-warn">On a Mac, Docker runs containers in a Linux VM that <b>cannot reach Metal</b>. Embeddings there are CPU-only — use the macOS app instead.</p>
        </div>
      </div>

      <div className="inst-main">
        <Steps steps={[
          {
            title: 'Run the installer',
            code: <>
              <span className="prompt">$</span> curl -fsSL {RAW}{'\n'}
              {'    '}/install-server.sh | bash{'\n'}
              <span className="comment"># picks docker or docker-gpu for your machine,</span>{'\n'}
              <span className="comment"># asks a few questions, brings the server up</span>
            </>,
            desc: <>One interactive installer for every mode. It checks prerequisites, writes the <code>.env</code>, starts the container, waits for health, then prints the dashboard URL and your admin login — and offers to install the <code>cix</code> CLI and connect it, so <code>cix init</code> works immediately. Re-run it to upgrade in place; <code>--uninstall</code> removes the server and keeps your data.</>,
          },
          {
            title: 'Or bring up compose yourself',
            code: <>
              <span className="prompt">$</span> git clone {GITHUB_URL}{'\n'}
              <span className="prompt">$</span> cd code-index && cp .env.example .env{'\n'}
              <span className="comment"># set CIX_BOOTSTRAP_ADMIN_EMAIL + _PASSWORD</span>{'\n'}
              <span className="prompt">$</span> docker compose pull{'\n'}
              <span className="prompt">$</span> docker compose up -d{'\n'}
              <span className="prompt">$</span> curl localhost:21847/health
            </>,
            desc: <>On a fresh database the server <b>refuses to start</b> without both bootstrap admin variables — it will not invent an account silently. <code>pull</code> is not optional: <code>up -d</code> alone reuses whatever image is already on the host, however old.</>,
          },
          {
            title: 'NVIDIA GPU instead',
            code: <>
              <span className="prompt">$</span> docker compose -f docker-compose.cuda.yml pull{'\n'}
              <span className="prompt">$</span> docker compose -f docker-compose.cuda.yml up -d
            </>,
            desc: <>The CUDA image sets <code>CIX_N_GPU_LAYERS=99</code> for full GPU offload of the embedding model. Same server, same version, same database — only the sidecar's compute differs.</>,
          },
          {
            title: 'Log in & set your password',
            code: <>
              <span className="prompt">$</span> open http://localhost:21847/dashboard{'\n'}
              <span className="comment"># sign in with the bootstrap admin</span>{'\n'}
              <span className="comment"># → you are asked to set a new password</span>
            </>,
            desc: <>The bootstrap password is temporary — the account is flagged <code>must_change_password</code>, so the dashboard makes you pick a real one before anything else. After that you can drop both bootstrap variables from the environment.</>,
          },
        ]} />
      </div>
    </div>
  );
}

function SourcePane() {
  return (
    <div className="inst-cols">
      <div className="inst-aside">
        <div className="inst-facts">
          <h4>You need</h4>
          <dl>
            <dt>Go 1.26+</dt>
            <dd>the server module's toolchain (the CLI builds on 1.25+)</dd>
            <dt>Node.js</dt>
            <dd>builds the dashboard that gets embedded into the binary</dd>
            <dt>Xcode Command Line Tools</dt>
            <dd>macOS only — <code>xcode-select --install</code></dd>
          </dl>
          <p>This is the path for <b>hacking on cix itself</b>, an unreleased branch, or a launchd agent under your own control.</p>
          <p>A container on a Mac runs inside a Linux VM that cannot reach Metal, so on a Mac a from-source build — or the app — is the only way to get GPU embeddings. On Linux with an NVIDIA card, the CUDA image gets you the same thing for less work.</p>
          <p className="inst-warn">Just want to run it on a Mac? The app installs the same natively-built, Metal-accelerated server with no toolchain at all.</p>
        </div>
      </div>

      <div className="inst-main">
        <Steps steps={[
          {
            title: 'Clone and let the installer do it',
            code: <>
              <span className="prompt">$</span> git clone {GITHUB_URL}{'\n'}
              <span className="prompt">$</span> cd code-index && ./install-server.sh{'\n'}
              <span className="comment"># mode: native (default on Apple Silicon)</span>{'\n'}
              <span className="comment"># builds the server + dashboard, fetches</span>{'\n'}
              <span className="comment"># llama-server, installs a launchd agent</span>
            </>,
            desc: <>The same installer as the Docker route, in its third mode. On Apple Silicon <code>native</code> is the default; every question has a sensible answer, so pressing Enter through all of them works. Configuration lands in <code>.env</code> at the repo root and the launchd agent re-reads it on every start.</>,
          },
          {
            title: 'Or build the pieces by hand',
            code: <>
              <span className="prompt">$</span> cd server && make bundle{'\n'}
              <span className="comment"># server + dashboard + Metal llama-server</span>{'\n'}
              <span className="prompt">$</span> cp ../.env.example ../.env{'\n'}
              <span className="prompt">$</span> make run{'\n'}
              <span className="prompt">$</span> cd ../cli && make build && make install
            </>,
            desc: <><code>make bundle</code> compiles <code>cix-server</code> with the embedded dashboard and downloads a Metal-enabled <code>llama-server</code> next to it — being siblings is what lets the server find the sidecar without any configuration. <code>make test</code> runs the suite.</>,
          },
          {
            title: 'Upgrade, restart, remove',
            code: <>
              <span className="prompt">$</span> git pull && ./install-server.sh{'\n'}
              <span className="prompt">$</span> tail -f ~/.cix/logs/cix-server.err{'\n'}
              <span className="prompt">$</span> launchctl kickstart -k \{'\n'}
              {'    '}gui/$(id -u)/com.cix.server{'\n'}
              <span className="prompt">$</span> ./install-server.sh --uninstall
            </>,
            desc: <>Re-running the installer after a pull upgrades in place. <code>--uninstall</code> removes the launchd agent and leaves your data and <code>.env</code> alone.</>,
          },
          {
            title: 'Log in & set your password',
            code: <>
              <span className="prompt">$</span> open http://localhost:21847/dashboard{'\n'}
              <span className="comment"># bootstrap admin from .env; the dashboard</span>{'\n'}
              <span className="comment"># forces a new password on first sign-in</span>
            </>,
            desc: <>Server v{SERVER_VERSION} is what a clone of <code>main</code> builds. Everyday management — logs, restart, uninstall — is <code>launchctl</code> against <code>com.cix.server</code>; the full list is in <code>doc/SETUP_MACOS_NATIVE.md</code>.</>,
          },
        ]} />
      </div>
    </div>
  );
}

const TABS = [
  { key: 'mac', label: 'macOS', sub: 'the app', Pane: MacPane },
  { key: 'docker', label: 'Docker', sub: 'CPU or CUDA', Pane: DockerPane },
  { key: 'source', label: 'Source', sub: 'from a checkout', Pane: SourcePane },
];

// Preselect what the visitor can actually run. The Mac tab stays first in the
// bar either way — it is the recommended install on the platform — but landing
// a Linux visitor on a DMG download button would be a dead end.
function defaultTab() {
  if (typeof navigator === 'undefined') return 'mac';
  const p = navigator.userAgentData?.platform || navigator.platform || navigator.userAgent || '';
  return /mac/i.test(p) ? 'mac' : 'docker';
}

export function InstallTabs() {
  const [active, setActive] = useState(defaultTab);
  const tab = TABS.find(t => t.key === active) || TABS[0];
  const { Pane } = tab;

  return (
    <div className="tabs install-tabs">
      <div className="tab-bar" role="tablist" aria-label="Installation method">
        {TABS.map(t => (
          <button
            key={t.key}
            role="tab"
            id={`inst-tab-${t.key}`}
            aria-selected={t.key === active}
            aria-controls={`inst-pane-${t.key}`}
            className={`tab-btn inst-tab ${t.key === active ? 'active' : ''}`}
            onClick={() => setActive(t.key)}
          >
            <b>{t.label}</b>
            <span className="inst-tab-sub">{t.sub}</span>
          </button>
        ))}
      </div>
      <div
        className="tab-body inst-body"
        role="tabpanel"
        id={`inst-pane-${tab.key}`}
        aria-labelledby={`inst-tab-${tab.key}`}
      >
        <Pane />
      </div>
    </div>
  );
}
