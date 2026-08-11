import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { Callout } from '@/ui/alert';
import { Status } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { Card, CardBody, CardHead, KV } from '@/ui/card';
import { Chip } from '@/ui/code';

// The effective public origin webhooks are delivered to, and where it came
// from. `tunnel` — a live managed tunnel; `public_url` — CIX_PUBLIC_URL is
// set, i.e. infrastructure (reverse proxy / ingress / static IP) already makes
// the server public and no tunnel is needed; `none` — nothing configured.
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

const ACTION_TONE: Record<ReconcileOutcome['action'], 'ok' | 'busy' | 'idle'> = {
  updated: 'ok',
  created: 'ok',
  skipped: 'idle',
  failed: 'busy',
};

// Shows the origin and lets an admin re-register every webhook_mode=auto repo
// against it. The tunnel that supplies that origin lives under Managed
// Tunnels — this tab only consumes it.
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
      setResult(await api.post<ReconcileResult>('/github/webhooks/reconcile'));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const configured = origin && origin.source !== 'none';

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardHead
          title="Delivery origin"
          aside={
            isAdmin ? (
              <Button size="sm" disabled={busy} onClick={reconcile}>
                {busy ? <Dots /> : null}
                Re-register webhooks
              </Button>
            ) : null
          }
        />
        <CardBody className="flex flex-col gap-3.5">
          <p className="m-0 text-[13.5px] text-dim">
            Hooks for <Chip>webhook_mode=auto</Chip> repositories are registered against this
            public origin. A live managed tunnel takes precedence over{' '}
            <Chip>CIX_PUBLIC_URL</Chip>. Re-registration runs on boot and whenever the tunnel URL
            changes{isAdmin ? ' — the button above forces it now.' : '; forcing it needs an admin.'}
          </p>

          {configured ? (
            <KV
              rows={[
                {
                  label: 'source',
                  value: origin!.source === 'tunnel' ? 'managed tunnel' : 'CIX_PUBLIC_URL',
                },
                { label: 'origin', value: origin!.origin, title: origin!.origin },
              ]}
            />
          ) : null}

          {origin?.source === 'public_url' ? (
            <p className="cix-hint m-0">
              your infrastructure already exposes the server, so a tunnel is optional — configure
              one under{' '}
              <Link to="/tunnels" className="text-accent hover:underline">
                Managed Tunnels
              </Link>{' '}
              only if the server should mint its own URL
            </p>
          ) : null}

          {!configured ? (
            <Callout variant="warn">
              <b>No public origin configured</b>
              <p>
                Webhook delivery needs one. Set <Chip>CIX_PUBLIC_URL</Chip> if the server is
                already reachable, or configure a tunnel under{' '}
                <Link to="/tunnels" className="text-accent hover:underline">
                  Managed Tunnels
                </Link>
                . Repositories where you are not an admin can sync by polling instead.
              </p>
            </Callout>
          ) : null}

          {err ? (
            <Callout variant="danger">
              <b>Re-registration failed</b>
              <p>{err}</p>
            </Callout>
          ) : null}
        </CardBody>
      </Card>

      {result ? (
        <Card>
          <CardHead
            title="Last re-registration"
            aside={
              <span className="font-mono text-[11.5px] font-normal text-muted">
                {result.total} auto · {result.created} created · {result.updated} updated ·{' '}
                {result.skipped} skipped · {result.failed} failed
              </span>
            }
          />
          {result.outcomes && result.outcomes.length > 0 ? (
            <>
              {result.outcomes.map((o) => (
                <div key={o.project_path} className="cix-row">
                  <span className="min-w-0 flex-1 truncate font-mono text-[13px]">
                    {o.project_path}
                  </span>
                  <Status tone={ACTION_TONE[o.action]} className="font-mono text-[11.5px]">
                    {o.action}
                    {o.note ? ` — ${o.note}` : ''}
                  </Status>
                </div>
              ))}
            </>
          ) : (
            <CardBody>
              <p className="m-0 text-sm text-dim">Nothing to re-register.</p>
            </CardBody>
          )}
        </Card>
      ) : null}
    </div>
  );
}
