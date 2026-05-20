import { useEffect, useState } from 'react';
import { AlertCircle, Cable, Copy, RefreshCw, Wifi } from 'lucide-react';
import { api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { Switch } from '@/ui/switch';
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

// ManagedTunnelsPage manages the server-orchestrated outbound tunnel that
// gives the server a public URL behind NAT. The feature has no env flag —
// everything (provider, enable, mode, hostname, token) is configured here
// and stored in the DB. One tunnel runs at a time; the provider is
// selectable (Cloudflare or ngrok).
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

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Managed Tunnels</h1>
        <p className="text-sm text-muted-foreground">
          A built-in outbound tunnel that gives this server a public URL while
          it sits behind NAT — no inbound ports required. Its first consumer is
          GitHub webhook delivery.
        </p>
      </header>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Could not load tunnel status</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {config === null ? (
        <Skeleton className="h-96 w-full" />
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
    </div>
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
    (config.provider as TunnelProvider) || 'cloudflare',
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
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <CardTitle className="flex items-center gap-2">
            <Cable className="size-4" />
            Tunnel
            {status && <TunnelStateBadge state={status.state} />}
          </CardTitle>
          {live && <TunnelActions onChanged={onChanged} />}
        </div>
        <CardDescription>
          One outbound tunnel managed by this server. Pick a provider, choose an
          ephemeral (dev) or stable (prod) URL, and save to apply.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Label>Provider</Label>
          <Select value={provider} onValueChange={(v) => setProvider(v as TunnelProvider)}>
            <SelectTrigger className="h-9">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="cloudflare">{PROVIDER_LABEL.cloudflare}</SelectItem>
              <SelectItem value="ngrok">{PROVIDER_LABEL.ngrok}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <BinarySection
          provider={provider}
          binary={binaries.find((b) => b.provider === provider)}
          onChanged={onChanged}
        />

        <div className="flex items-center justify-between gap-3">
          <div>
            <Label htmlFor="tun-enabled">Enabled</Label>
            <p className="text-xs text-muted-foreground">Start the tunnel on this server.</p>
          </div>
          <Switch id="tun-enabled" checked={enabled} onCheckedChange={setEnabled} />
        </div>

        <div className="space-y-1">
          <Label>Mode</Label>
          <Select value={mode} onValueChange={(v) => setMode(v as TunnelMode)}>
            <SelectTrigger className="h-9">
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
        </div>

        {showHostname && (
          <div className="space-y-1">
            <Label htmlFor="tun-host">{hostnameLabel}</Label>
            <Input
              id="tun-host"
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
              placeholder={hostnamePlaceholder}
            />
          </div>
        )}

        {showToken && (
          <div className="space-y-1">
            <Label htmlFor="tun-token">{tokenLabel}</Label>
            <Input
              id="tun-token"
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder={config.token_set ? '•••••• (leave blank to keep)' : tokenLabel}
              className="font-mono"
            />
            <p className="text-xs text-muted-foreground">
              Stored encrypted-at-rest.
              {provider === 'ngrok'
                ? ' From your ngrok dashboard (Your Authtoken).'
                : ' From cloudflared tunnel token <name>.'}
            </p>
          </div>
        )}

        {err && (
          <Alert variant="destructive">
            <AlertCircle className="size-4" />
            <AlertDescription>{err}</AlertDescription>
          </Alert>
        )}

        <div className="flex justify-end">
          <Button onClick={save} disabled={saving}>
            {saving ? 'Applying…' : 'Save & apply'}
          </Button>
        </div>

        {live && status && <StatusDetails status={status} />}
      </CardContent>
    </Card>
  );
}

function StatusDetails({ status }: { status: TunnelStatus }) {
  return (
    <div className="space-y-2 border-t pt-4">
      <Field label="Active provider" value={status.provider} />
      <Field label="Mode" value={status.mode ?? '—'} />
      <PublicUrlField url={status.public_url} />
      <Field label="PID" value={status.pid ? String(status.pid) : '—'} />
      <Field label="Uptime" value={formatUptime(status.uptime_sec)} />
      {status.last_error && (
        <Alert variant="destructive">
          <AlertCircle className="size-4" />
          <AlertTitle>Last error</AlertTitle>
          <AlertDescription className="font-mono text-xs">{status.last_error}</AlertDescription>
        </Alert>
      )}
    </div>
  );
}

function TunnelActions({ onChanged }: { onChanged: () => void }) {
  const [busy, setBusy] = useState<'test' | 'restart' | null>(null);
  const [result, setResult] = useState<TunnelTestResult | null>(null);
  const [err, setErr] = useState<string | null>(null);

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
    if (!confirm('Restart the tunnel? Webhooks re-register automatically if the URL changes.')) return;
    setBusy('restart');
    setErr(null);
    try {
      await api.post('/tunnels/restart');
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex gap-2">
        <Button variant="outline" size="sm" disabled={busy !== null} onClick={runTest}>
          <Wifi className="mr-1 size-4" />
          {busy === 'test' ? 'Testing…' : 'Test connection'}
        </Button>
        <Button variant="outline" size="sm" disabled={busy !== null} onClick={restart}>
          <RefreshCw className="mr-1 size-4" />
          {busy === 'restart' ? 'Restarting…' : 'Restart'}
        </Button>
      </div>
      {result && (
        <span className={`text-xs ${result.ok ? 'text-green-600' : 'text-destructive'}`}>
          {result.ok ? '✓' : '✗'} {result.detail}
          {result.latency_ms != null && ` (${result.latency_ms} ms)`}
        </span>
      )}
      {err && <span className="text-xs text-destructive">{err}</span>}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono capitalize">{value}</span>
    </div>
  );
}

function PublicUrlField({ url }: { url?: string }) {
  if (!url) {
    return <Field label="Public URL" value="(not assigned yet)" />;
  }
  return (
    <div className="flex items-center justify-between gap-3 text-sm">
      <span className="text-muted-foreground">Public URL</span>
      <span className="flex items-center gap-1">
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="truncate font-mono text-primary underline-offset-2 hover:underline"
        >
          {url}
        </a>
        <Button
          variant="ghost"
          size="sm"
          className="size-7 p-0"
          onClick={() => void navigator.clipboard?.writeText(url)}
          title="Copy URL"
        >
          <Copy className="size-3.5" />
        </Button>
      </span>
    </div>
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
