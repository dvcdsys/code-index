import { NavLink } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { useServerStatus } from '@/lib/useServerStatus';
import { Card, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { cn } from '@/lib/cn';
import { MODULES } from '../registry';

// One-line pitch per module — kept here (not on the Module type) so the
// sidebar stays terse and only the landing page carries the prose.
const DESCRIPTIONS: Record<string, string> = {
  projects:
    'Browse indexed repositories, inspect stats, copy reindex commands, and watch for stale-model drift.',
  search:
    'Five modes — semantic, symbols, references, definitions, files — across every project from one bar.',
  'api-keys':
    'Mint long-lived API keys for CLI / CI use, scope them to a role, revoke at any time.',
  users: 'Invite teammates, set roles, reset passwords, and audit access.',
  settings: 'Personal preferences — theme, default editor, change password.',
  server:
    'Tune the embedding model and llama-server runtime, restart the sidecar without dropping into SSH.',
};

export default function HomePage() {
  const { user } = useAuth();
  const role = user?.role ?? 'viewer';
  const { data: status } = useServerStatus();

  const cards = MODULES.filter((m) => m.id !== 'home').filter((m) => {
    if (!m.requiredRole) return true;
    if (m.requiredRole === 'viewer') return true;
    return role === 'admin';
  });

  return (
    <div className="space-y-8">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">
          Welcome back{user?.email ? `, ${user.email}` : ''}
        </h1>
        <p className="text-sm text-muted-foreground">
          The cix dashboard — semantic code search, project management, and runtime control.
        </p>
      </header>

      {status && (
        <div className="grid gap-3 rounded-lg border bg-muted/30 p-4 sm:grid-cols-3">
          <StatusStat label="Server" value={`v${status.server_version}`} />
          <StatusStat label="Embedding model" value={status.embedding_model || '—'} mono />
          <StatusStat
            label="Sidecar"
            value={status.model_loaded ? 'Ready' : 'Loading…'}
            tone={status.model_loaded ? 'ok' : 'warn'}
          />
        </div>
      )}

      <div>
        <h2 className="mb-3 text-sm font-medium text-muted-foreground">Modules</h2>
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {cards.map((m) => {
            const Icon = m.icon;
            return (
              <NavLink key={m.id} to={m.path} className="group focus:outline-none">
                <Card
                  className={cn(
                    'h-full transition-colors',
                    'group-hover:border-foreground/30 group-hover:bg-accent/40',
                    'group-focus-visible:ring-2 group-focus-visible:ring-ring'
                  )}
                >
                  <CardHeader>
                    <div className="flex items-center gap-2">
                      <Icon className="h-4 w-4 text-muted-foreground" />
                      <CardTitle className="text-base">{m.label}</CardTitle>
                    </div>
                    <CardDescription>{DESCRIPTIONS[m.id] ?? ''}</CardDescription>
                  </CardHeader>
                </Card>
              </NavLink>
            );
          })}
        </div>
      </div>

      <p className="text-xs text-muted-foreground">
        Prefer the terminal? <code className="rounded bg-muted px-1 py-0.5">cix --help</code>{' '}
        does everything the dashboard does, plus reindex, watch, and bulk operations.
      </p>
    </div>
  );
}

function StatusStat({
  label,
  value,
  mono,
  tone,
}: {
  label: string;
  value: string;
  mono?: boolean;
  tone?: 'ok' | 'warn';
}) {
  return (
    <div className="min-w-0">
      <div className="text-xs uppercase tracking-wider text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-1 truncate text-sm font-medium',
          mono && 'font-mono text-xs',
          tone === 'ok' && 'text-emerald-600 dark:text-emerald-400',
          tone === 'warn' && 'text-amber-600 dark:text-amber-400'
        )}
        title={value}
      >
        {value}
      </div>
    </div>
  );
}
