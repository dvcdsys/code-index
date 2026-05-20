import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertCircle, RefreshCw } from 'lucide-react';
import { api } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';

// Effective public origin webhook URLs are delivered to, and where it comes
// from. `tunnel` — a live managed tunnel; `public_url` — CIX_PUBLIC_URL set,
// i.e. the server is made public by infrastructure (reverse proxy / ingress /
// static IP) and a tunnel is NOT needed; `none` — no origin configured.
type WebhookOriginSource = 'tunnel' | 'public_url' | 'none';
type WebhookOrigin = { origin: string; source: WebhookOriginSource };

type ReconcileOutcome = {
  project_path: string;
  action: 'updated' | 'created' | 'skipped' | 'failed';
  note?: string;
};

type ReconcileResult = {
  base_url: string;
  total: number;
  created: number;
  updated: number;
  skipped: number;
  failed: number;
  outcomes?: ReconcileOutcome[];
};

const ACTION_COLOR: Record<ReconcileOutcome['action'], string> = {
  updated: 'text-green-600',
  created: 'text-green-600',
  skipped: 'text-muted-foreground',
  failed: 'text-destructive',
};

// WebhooksTab shows the public origin webhooks are delivered to and lets the
// operator re-register every webhook_mode=auto repo against it. The tunnel
// that provides the public URL is managed under Managed Tunnels — this tab
// only consumes it.
export default function WebhooksTab() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [origin, setOrigin] = useState<WebhookOrigin | null>(null);
  const [result, setResult] = useState<ReconcileResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void api
      .get<WebhookOrigin>('/github/webhooks/origin')
      .then(setOrigin)
      .catch(() => setOrigin(null));
  }, []);

  async function reconcile() {
    setBusy(true);
    setErr(null);
    setResult(null);
    try {
      const res = await api.post<ReconcileResult>('/github/webhooks/reconcile');
      setResult(res);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Webhook delivery origin</CardTitle>
          <CardDescription>
            GitHub webhooks for <code>webhook_mode=auto</code> repos are
            registered against this public origin. A live managed tunnel takes
            precedence over <code>CIX_PUBLIC_URL</code>.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          {origin?.source === 'tunnel' && (
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">Active tunnel URL</span>
              <span className="font-mono">{origin.origin}</span>
            </div>
          )}

          {origin?.source === 'public_url' && (
            <div className="space-y-1">
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground">
                  Delivery origin (<code>CIX_PUBLIC_URL</code>)
                </span>
                <span className="font-mono">{origin.origin}</span>
              </div>
              <p className="text-xs text-muted-foreground">
                The server is made publicly reachable by your infrastructure,
                so a managed tunnel is optional. Configure one under{' '}
                <Link to="/tunnels" className="text-primary underline-offset-2 hover:underline">
                  Managed Tunnels
                </Link>{' '}
                only if you need the server to mint its own public URL.
              </p>
            </div>
          )}

          {(!origin || origin.source === 'none') && (
            <Alert>
              <AlertCircle className="size-4" />
              <AlertTitle>No public origin configured</AlertTitle>
              <AlertDescription>
                Webhook delivery needs a public URL. Set{' '}
                <code>CIX_PUBLIC_URL</code> if the server is already reachable
                (reverse proxy, ingress, static IP), or configure a tunnel
                under{' '}
                <Link to="/tunnels" className="text-primary underline-offset-2 hover:underline">
                  Managed Tunnels
                </Link>
                . Repos where you aren't an admin can sync via polling instead.
              </AlertDescription>
            </Alert>
          )}

          <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
            <p className="text-xs text-muted-foreground">
              Re-registration runs automatically on boot and when the tunnel URL
              changes.{' '}
              {isAdmin ? 'Use this to trigger it manually.' : 'Manual re-registration requires an admin.'}
            </p>
            {isAdmin && (
              <Button variant="outline" size="sm" disabled={busy} onClick={reconcile}>
                <RefreshCw className="mr-1 size-4" />
                {busy ? 'Re-registering…' : 'Re-register webhooks'}
              </Button>
            )}
          </div>

          {err && (
            <Alert variant="destructive">
              <AlertCircle className="size-4" />
              <AlertTitle>Reconcile failed</AlertTitle>
              <AlertDescription>{err}</AlertDescription>
            </Alert>
          )}
        </CardContent>
      </Card>

      {result && (
        <Card>
          <CardHeader>
            <CardTitle>Last reconcile</CardTitle>
            <CardDescription>
              {result.total} auto repo(s) · {result.updated} updated ·{' '}
              {result.created} created · {result.skipped} skipped ·{' '}
              {result.failed} failed
            </CardDescription>
          </CardHeader>
          {result.outcomes && result.outcomes.length > 0 && (
            <CardContent>
              <ul className="divide-y rounded-md border">
                {result.outcomes.map((o) => (
                  <li
                    key={o.project_path}
                    className="flex items-center justify-between gap-3 px-4 py-2 text-sm"
                  >
                    <span className="truncate font-mono text-xs">{o.project_path}</span>
                    <span className={`shrink-0 capitalize ${ACTION_COLOR[o.action]}`}>
                      {o.action}
                      {o.note ? ` — ${o.note}` : ''}
                    </span>
                  </li>
                ))}
              </ul>
            </CardContent>
          )}
        </Card>
      )}
    </div>
  );
}
