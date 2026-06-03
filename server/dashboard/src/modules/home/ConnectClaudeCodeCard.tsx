import { useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { ChevronDown, ChevronRight, KeyRound, Sparkles } from 'lucide-react';
import { Card } from '@/ui/card';
import { Badge } from '@/ui/badge';
import { cn } from '@/lib/cn';
import { cixConnectCommand } from '@/lib/cixServer';
import { CommandBlock } from './CommandBlock';

// Collapse state lives in localStorage so a returning user who dismissed the
// onboarding card keeps it dismissed. Expanded by default (key absent → false).
const COLLAPSE_KEY = 'cix.home.connect-claude.collapsed';

function readCollapsed(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return window.localStorage.getItem(COLLAPSE_KEY) === '1';
  } catch {
    return false;
  }
}

function writeCollapsed(v: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(COLLAPSE_KEY, v ? '1' : '0');
  } catch {
    /* swallow — privacy mode; state just resets on reload */
  }
}

// The cix CLI one-line installer (README §3 "Install the CLI").
const INSTALL_CMD =
  'curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash';

// Claude Code marketplace + plugin install, as `claude plugin` console
// commands (run in a terminal) rather than the in-session `/plugin` slash
// commands — so the whole onboarding flow stays in the shell next to the CLI
// steps. `marketplace add` takes a GitHub owner/repo shorthand; `install`
// takes plugin@marketplace. Installing via the CLI applies on the next
// `claude` session start, so there's no `/reload-plugins` equivalent to run.
const PLUGIN_CMDS = [
  'claude plugin marketplace add dvcdsys/code-index',
  'claude plugin install cix@code-index',
].join('\n');

// Optional local-project indexing + connection check.
const INDEX_CMDS = ['cd /path/to/your/project', 'cix init', 'cix status   # wait for: Status: ✓ Indexed'].join(
  '\n'
);

// User-facing skills/commands the plugin exposes — the "success" checklist.
const SKILL_COMMANDS = [
  '/cix:search',
  '/cix:def',
  '/cix:refs',
  '/cix:init',
  '/cix:status',
  '/cix:summary',
];

export function ConnectClaudeCodeCard() {
  const [collapsed, setCollapsed] = useState(readCollapsed);

  function toggle() {
    setCollapsed((prev) => {
      const next = !prev;
      writeCollapsed(next);
      return next;
    });
  }

  // Live, per-host connect command so the alias + URL match this exact
  // deployment — the same derivation the API-key popup uses. The real key is
  // shown once in that popup; here we keep the `<key>` placeholder.
  const connectCmd = cixConnectCommand(window.location.origin, window.location.host);

  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={!collapsed}
        className="flex w-full items-center gap-2 p-4 text-left transition-colors hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {collapsed ? (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        )}
        <Sparkles className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="flex-1">
          <span className="block text-base font-semibold leading-none tracking-tight">
            Connect Claude Code to cix
          </span>
          <span className="mt-1 block text-sm text-muted-foreground">
            Install the CLI, link this server, add the plugin — then the cix skills show up in
            Claude Code.
          </span>
        </span>
      </button>

      {!collapsed && (
        <div className="space-y-6 border-t px-4 pb-5 pt-4">
          <Step
            n={1}
            title="Install the cix CLI"
            hint="Run in a terminal on macOS or Linux."
          >
            <CommandBlock command={INSTALL_CMD} />
          </Step>

          <Step
            n={2}
            title="Connect the CLI to this server"
            hint={
              <>
                Open the{' '}
                <Link
                  to="/api-keys"
                  className="inline-flex items-center gap-1 font-medium text-primary underline-offset-4 hover:underline"
                >
                  <KeyRound className="h-3.5 w-3.5" />
                  API Keys
                </Link>{' '}
                page, create a key, then copy the “Connect the cix CLI” command from the popup.
                It looks like this for this server (with your real key filled in):
              </>
            }
          >
            <CommandBlock command={connectCmd} />
          </Step>

          <Step
            n={3}
            title="Add the plugin to Claude Code"
            hint="Run these in a terminal (not inside a Claude Code session). The first registers this repo as a plugin marketplace; the second installs the cix plugin. It activates automatically the next time you start claude — no reload needed."
          >
            <CommandBlock command={PLUGIN_CMDS} />
          </Step>

          <Step
            n={4}
            title="Index a project"
            badge={<Badge variant="secondary">Optional</Badge>}
            hint={
              <>
                Skip this if you only work with{' '}
                <Link to="/workspaces" className="font-medium text-primary underline-offset-4 hover:underline">
                  workspaces
                </Link>{' '}
                or server-side{' '}
                <Link to="/projects" className="font-medium text-primary underline-offset-4 hover:underline">
                  external projects
                </Link>
                . <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">cix status</code> alone
                still confirms the CLI reached the server.
              </>
            }
          >
            <CommandBlock command={INDEX_CMDS} />
          </Step>

          <Step
            n={5}
            title="See the cix skills in Claude Code"
            hint="Once the plugin is installed and you start a new claude session, these commands and the cix / cix-workspace skills are available — that's the finish line:"
          >
            <div className="flex flex-wrap gap-1.5">
              {SKILL_COMMANDS.map((c) => (
                <code
                  key={c}
                  className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground"
                >
                  {c}
                </code>
              ))}
            </div>
          </Step>
        </div>
      )}
    </Card>
  );
}

function Step({
  n,
  title,
  hint,
  badge,
  children,
}: {
  n: number;
  title: string;
  hint: ReactNode;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex gap-3">
      <div
        className={cn(
          'flex h-6 w-6 shrink-0 items-center justify-center rounded-full',
          'border bg-muted text-xs font-semibold text-muted-foreground'
        )}
        aria-hidden
      >
        {n}
      </div>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-medium">{title}</h3>
          {badge}
        </div>
        <p className="text-sm text-muted-foreground">{hint}</p>
        {children}
      </div>
    </div>
  );
}
