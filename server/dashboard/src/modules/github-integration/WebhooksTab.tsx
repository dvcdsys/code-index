import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertCircle, RefreshCw } from 'lucide-react';
import { api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import type { TunnelStatus } from '../managed-tunnels/types';

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
  const [tunnel, setTunnel] = useState<TunnelStatus | null>(null);
  const [result, setResult] = useState<ReconcileResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void api
      .get<TunnelStatus>('/tunnels/status')
      .then(setTunnel)
      .catch(() => setTunnel(null));
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

  const tunnelLive = tunnel?.state === 'live' && !!tunnel.public_url;

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
          {tunnelLive ? (
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">Active tunnel URL</span>
              <span className="font-mono">{tunnel?.public_url}</span>
            </div>
          ) : (
            <Alert>
              <AlertCircle className="size-4" />
              <AlertTitle>No live tunnel</AlertTitle>
              <AlertDescription>
                There is no active tunnel URL. Webhooks fall back to{' '}
                <code>CIX_PUBLIC_URL</code> if set. Configure a tunnel under{' '}
                <Link to="/tunnels" className="text-primary underline-offset-2 hover:underline">
                  Managed Tunnels
                </Link>
                .
              </AlertDescription>
            </Alert>
          )}

          <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
            <p className="text-xs text-muted-foreground">
              Re-registration runs automatically on boot and when the tunnel URL
              changes. Use this to trigger it manually.
            </p>
            <Button variant="outline" size="sm" disabled={busy} onClick={reconcile}>
              <RefreshCw className="mr-1 size-4" />
              {busy ? 'Re-registering…' : 'Re-register webhooks'}
            </Button>
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
