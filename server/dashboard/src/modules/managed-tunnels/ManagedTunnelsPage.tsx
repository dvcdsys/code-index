import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Status } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { Card, CardBody, CardHead, KV } from '@/ui/card';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';
import { Field, Input } from '@/ui/input';
import { Page } from '@/ui/page';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { SwitchRow } from '@/ui/switch';
import { useCopy } from '@/lib/useCopy';
import { BinarySection } from './BinarySection';
import { TunnelStateBadge } from './TunnelStateBadge';
import type {
  TunnelBinary,
  TunnelBinaryList,
  TunnelConfig,
  TunnelConfigUpdate,
  TunnelMode,
  TunnelProvider,
  TunnelStatus,
  TunnelTestResult,
} from './types';

// The server-orchestrated outbound tunnel that gives this server a public URL
// from behind NAT. There is no env flag — provider, enable, mode, hostname and
// token all live in the DB and are configured here. One tunnel at a time.
export default function ManagedTunnelsPage() {
  const [config, setConfig] = useState<TunnelConfig | null>(null);
  const [status, setStatus] = useState<TunnelStatus | null>(null);
  const [binaries, setBinaries] = useState<TunnelBinary[]>([]);
  const [error, setError] = useState<string | null>(null);

  async function reload() {
    try {
      const [cfg, st, bins] = await Promise.all([
        api.get<TunnelConfig>('/tunnels/config'),
        api.get<TunnelStatus>('/tunnels/status'),
        api.get<TunnelBinaryList>('/tunnels/binaries'),
      ]);
      setConfig(cfg);
      setStatus(st);
      setBinaries(bins.binaries);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    void reload();
  }, []);

  useStatusFact(status ? `tunnel ${status.state}` : null);

  return (
    <Page
      title="Managed tunnels"
      subtitle="A built-in outbound tunnel that gives this server a public URL from behind NAT — no inbound ports. Its first consumer is GitHub webhook delivery."
    >
      {error ? (
        <Callout variant="danger" className="mb-5">
          <b>Could not load the tunnel status</b>
          <p>{error}</p>
        </Callout>
      ) : null}

      {config === null ? (
        <Skeleton className="h-96" />
      ) : (
        <TunnelConfigCard
          config={config}
          status={status}
          binaries={binaries}
          onApplied={(st) => {
            setStatus(st);
            void reload();
          }}
          onChanged={reload}
        />
      )}
    </Page>
  );
}

const PROVIDER_LABEL: Record<TunnelProvider, string> = {
  cloudflare: 'Cloudflare Tunnel',
  ngrok: 'ngrok',
};

function modeOptions(provider: TunnelProvider): { value: TunnelMode; label: string }[] {
  if (provider === 'ngrok') {
    return [
      { value: 'quick', label: 'Ephemeral URL (dev)' },
      { value: 'named', label: 'Reserved domain (prod)' },
    ];
  }
  return [
    { value: 'quick', label: 'Quick — ephemeral (dev)' },
    { value: 'named', label: 'Named — stable hostname (prod)' },
  ];
}

function TunnelConfigCard({
  config,
  status,
  binaries,
  onApplied,
  onChanged,
}: {
  config: TunnelConfig;
  status: TunnelStatus | null;
  binaries: TunnelBinary[];
  onApplied: (st: TunnelStatus) => void;
  onChanged: () => void;
}) {
  const [provider, setProvider] = useState<TunnelProvider>(
    (config.provider as TunnelProvider) || 'cloudflare'
  );
  const [enabled, setEnabled] = useState(config.enabled);
  const [mode, setMode] = useState<TunnelMode>(config.mode);
  const [hostname, setHostname] = useState(config.hostname);
  const [token, setToken] = useState('');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    setSaving(true);
    setErr(null);
    const body: TunnelConfigUpdate = { enabled, provider, mode, hostname };
    if (token.trim() !== '') body.token = token;
    try {
      const st = await api.put<TunnelStatus>('/tunnels/config', body);
      setToken('');
      onApplied(st);
      toast.success('Tunnel configuration applied');
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  const live = status && status.state !== 'disabled';
  // ngrok always needs an authtoken; cloudflare only in named mode.
  const showToken = provider === 'ngrok' || mode === 'named';
  const showHostname = mode === 'named';
  const hostnameLabel = provider === 'ngrok' ? 'Reserved domain' : 'Public hostname';
  const hostnamePlaceholder = provider === 'ngrok' ? 'your-domain.ngrok.app' : 'cix.example.com';
  const tokenLabel = provider === 'ngrok' ? 'Authtoken' : 'Connector token';

  return (
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
      <Card>
        <CardHead
          title="Tunnel"
          aside={
            <Button variant="primary" onClick={save} disabled={saving}>
              {saving ? <Dots /> : null}
              Save &amp; apply
            </Button>
          }
        >
          {status ? <TunnelStateBadge state={status.state} /> : null}
        </CardHead>
        <CardBody className="flex flex-col gap-4">
          <p className="m-0 text-[13.5px] text-dim">
            One outbound tunnel, managed by this server. Pick a provider, choose an ephemeral (dev)
            or stable (prod) URL, then save to apply.
          </p>

          <Field label="Provider">
            <Select value={provider} onValueChange={(v) => setProvider(v as TunnelProvider)}>
              <SelectTrigger aria-label="Tunnel provider">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="cloudflare">{PROVIDER_LABEL.cloudflare}</SelectItem>
                <SelectItem value="ngrok">{PROVIDER_LABEL.ngrok}</SelectItem>
              </SelectContent>
            </Select>
          </Field>

          <BinarySection
            provider={provider}
            binary={binaries.find((b) => b.provider === provider)}
            onChanged={onChanged}
          />

          <SwitchRow
            id="tun-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
            label="Enabled"
            hint="Start the tunnel on this server."
          />

          <Field label="Mode">
            <Select value={mode} onValueChange={(v) => setMode(v as TunnelMode)}>
              <SelectTrigger aria-label="Tunnel mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {modeOptions(provider).map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {showHostname ? (
            <Field label={hostnameLabel} htmlFor="tun-host">
              <Input
                id="tun-host"
                value={hostname}
                onChange={(e) => setHostname(e.target.value)}
                placeholder={hostnamePlaceholder}
              />
            </Field>
          ) : null}

          {showToken ? (
            <Field
              label={tokenLabel}
              htmlFor="tun-token"
              hint={
                provider === 'ngrok'
                  ? 'Stored encrypted at rest. From your ngrok dashboard (Your Authtoken).'
                  : 'Stored encrypted at rest. From `cloudflared tunnel token <name>`.'
              }
            >
              <Input
                id="tun-token"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={config.token_set ? '•••••• leave blank to keep' : tokenLabel}
              />
            </Field>
          ) : null}

          {err ? (
            <Callout variant="danger">
              <p>{err}</p>
            </Callout>
          ) : null}
        </CardBody>
      </Card>

      {live && status ? (
        <div className="flex flex-col gap-5">
          <StatusCard status={status} onChanged={onChanged} />
        </div>
      ) : null}
    </div>
  );
}

// Live runtime state in the right rail — visible while the form on the left is
// being edited, same as the sidecar rail on the Server page.
function StatusCard({ status, onChanged }: { status: TunnelStatus; onChanged: () => void }) {
  const [busy, setBusy] = useState<'test' | 'restart' | null>(null);
  const [result, setResult] = useState<TunnelTestResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [confirmRestart, setConfirmRestart] = useState(false);
  const url = useCopy();

  async function runTest() {
    setBusy('test');
    setErr(null);
    setResult(null);
    try {
      setResult(await api.post<TunnelTestResult>('/tunnels/test'));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function restart() {
    setBusy('restart');
    setErr(null);
    try {
      await api.post('/tunnels/restart');
      setConfirmRestart(false);
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card>
      <CardHead>
        <TunnelStateBadge state={status.state} />
      </CardHead>
      <CardBody className="flex flex-col gap-3.5">
        <KV
          rows={[
            { label: 'provider', value: status.provider },
            { label: 'mode', value: status.mode ?? '—' },
            { label: 'pid', value: status.pid ? String(status.pid) : '—' },
            { label: 'uptime', value: formatUptime(status.uptime_sec) },
          ]}
        />

        <div>
          <span className="cix-label">Public URL</span>
          {status.public_url ? (
            <div className="mt-1.5 flex items-center gap-2">
              <a
                href={status.public_url}
                target="_blank"
                rel="noreferrer"
                className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-accent hover:underline"
              >
                {status.public_url}
              </a>
              <Button size="sm" onClick={() => void url.copy(status.public_url!)}>
                {url.copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
          ) : (
            <p className="cix-hint mt-1.5">not assigned yet</p>
          )}
        </div>

        <div className="flex gap-2">
          <Button size="sm" className="flex-1" disabled={busy !== null} onClick={runTest}>
            {busy === 'test' ? <Dots /> : null}
            Test
          </Button>
          <Button
            size="sm"
            className="flex-1"
            disabled={busy !== null}
            onClick={() => setConfirmRestart(true)}
          >
            Restart
          </Button>
        </div>

        {result ? (
          <Status tone={result.ok ? 'ok' : 'busy'} className="font-mono text-[11.5px]">
            {result.detail}
            {result.latency_ms != null ? ` · ${result.latency_ms} ms` : ''}
          </Status>
        ) : null}

        {err ? <span className="cix-error">{err}</span> : null}

        {status.last_error ? (
          <Callout variant="danger">
            <b>Last error</b>
            <p className="font-mono text-[11.5px]">{status.last_error}</p>
          </Callout>
        ) : null}
      </CardBody>

      <Dialog
        open={confirmRestart}
        onOpenChange={(next) => (busy === null ? setConfirmRestart(next) : null)}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Restart the tunnel?</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              Delivery pauses for a few seconds. If the public URL changes, webhooks re-register
              themselves against the new one.
            </DialogDescription>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmRestart(false)} disabled={busy !== null}>
              Cancel
            </Button>
            <Button variant="danger" onClick={restart} disabled={busy !== null}>
              {busy === 'restart' ? <Dots /> : null}
              Restart
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function formatUptime(sec?: number): string {
  if (!sec || sec <= 0) return '—';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}
