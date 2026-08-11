import { useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Card, CardHead } from '@/ui/card';
import { Badge } from '@/ui/badge';
import { CodeBlock, Chip } from '@/ui/code';
import { Step } from '@/ui/progress';
import { cixConnectCommand } from '@/lib/cixServer';

// Collapse state lives in localStorage so a returning user who folded the
// onboarding card keeps it folded. Expanded by default (key absent → false).
const COLLAPSE_KEY = 'cix.home.connect-claude.collapsed';

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(COLLAPSE_KEY) === '1';
  } catch {
    return false;
  }
}

function writeCollapsed(v: boolean): void {
  try {
    window.localStorage.setItem(COLLAPSE_KEY, v ? '1' : '0');
  } catch {
    /* privacy mode — the state just resets on reload */
  }
}

// The cix CLI one-line installer (README §3 "Install the CLI").
const INSTALL_CMD =
  'curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash';

// Claude Code marketplace + plugin install as `claude plugin` console commands
// (run in a terminal) rather than the in-session `/plugin` slash commands — so
// the whole flow stays in the shell next to the CLI steps. Installing via the
// CLI applies on the next `claude` session start; there is no reload to run.
const PLUGIN_CMDS = [
  'claude plugin marketplace add dvcdsys/code-index',
  'claude plugin install cix@code-index',
].join('\n');

const INDEX_CMDS = ['cd /path/to/your/project', 'cix init', 'cix status'].join('\n');

const SKILL_COMMANDS = [
  '/cix:search',
  '/cix:def',
  '/cix:refs',
  '/cix:init',
  '/cix:status',
  '/cix:summary',
];

// Four numbered steps, each with its command attached to a Copy button — the
// old version buried the same commands in prose. Steps 3 and 4 stay folded
// because they are optional detail, not blocking work.
export function ConnectClaudeCode() {
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [showPlugin, setShowPlugin] = useState(false);
  const [showIndex, setShowIndex] = useState(false);

  function toggle() {
    setCollapsed((prev) => {
      const next = !prev;
      writeCollapsed(next);
      return next;
    });
  }

  // Live, per-host connect command so the alias + URL match this exact
  // deployment — the same derivation the API-key popup uses. The real key is
  // shown once there; here the `<key>` placeholder stands in.
  const connectCmd = cixConnectCommand(window.location.origin, window.location.host);

  return (
    <Card className="min-w-0 self-start">
      <CardHead className="cursor-pointer select-none" onClick={toggle}>
        <span>Connect Claude Code to cix</span>
        <Badge variant="outline">4 steps</Badge>
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-label={collapsed ? 'Expand' : 'Collapse'}
          className="ml-auto font-mono text-xs text-muted"
        >
          {collapsed ? '▸' : '▾'}
        </button>
      </CardHead>

      {!collapsed && (
        <div className="flex flex-col gap-4 px-[18px] py-4">
          <Step n={1}>
            <StepTitle>Install the cix CLI</StepTitle>
            <CodeBlock command={INSTALL_CMD} className="mt-2" />
          </Step>

          <Step n={2}>
            <StepTitle>Connect the CLI to this server</StepTitle>
            <CodeBlock command={connectCmd} className="mt-2" />
            <p className="mt-2 text-[13px] text-dim">
              Create a key on{' '}
              <Link to="/api-keys" className="text-accent hover:underline">
                API Keys
              </Link>{' '}
              and paste the command from the popup — it is this one with the real key filled in.
            </p>
          </Step>

          <Step n={3} pending={!showPlugin}>
            <div className="flex items-center gap-3">
              <StepTitle muted={!showPlugin}>Add the plugin to Claude Code</StepTitle>
              <button
                type="button"
                className="ml-auto font-mono text-[11px] text-muted hover:text-ink"
                onClick={() => setShowPlugin((v) => !v)}
              >
                {showPlugin ? 'hide' : 'show'}
              </button>
            </div>
            {showPlugin && (
              <>
                <CodeBlock command={PLUGIN_CMDS} className="mt-2" wrap />
                <p className="mt-2 text-[13px] text-dim">
                  Run these in a terminal, not inside a Claude Code session. The plugin activates
                  the next time you start <Chip>claude</Chip>, and these commands appear:
                </p>
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {SKILL_COMMANDS.map((c) => (
                    <Chip key={c}>{c}</Chip>
                  ))}
                </div>
              </>
            )}
          </Step>

          <Step n={4} pending={!showIndex}>
            <div className="flex items-center gap-3">
              <StepTitle muted={!showIndex}>Index a project</StepTitle>
              <Badge variant="quiet">optional</Badge>
              <button
                type="button"
                className="ml-auto font-mono text-[11px] text-muted hover:text-ink"
                onClick={() => setShowIndex((v) => !v)}
              >
                {showIndex ? 'hide' : 'show'}
              </button>
            </div>
            {showIndex && (
              <>
                <CodeBlock command={INDEX_CMDS} className="mt-2" wrap />
                <p className="mt-2 text-[13px] text-dim">
                  Skip it if you only use{' '}
                  <Link to="/workspaces" className="text-accent hover:underline">
                    workspaces
                  </Link>{' '}
                  or server-side{' '}
                  <Link to="/projects" className="text-accent hover:underline">
                    external projects
                  </Link>
                  . <Chip>cix status</Chip> alone still proves the CLI reached this server.
                </p>
              </>
            )}
          </Step>
        </div>
      )}
    </Card>
  );
}

function StepTitle({ children, muted }: { children: ReactNode; muted?: boolean }) {
  return (
    <span className={`text-[14.5px] font-semibold ${muted ? 'text-dim' : ''}`}>{children}</span>
  );
}
