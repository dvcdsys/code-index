import { useState } from 'react';
import { AlertCircle, CheckCircle2, Download } from 'lucide-react';
import { api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import type { TunnelBinary, TunnelProvider } from './types';

// Per-provider manual install instructions, shown only for local
// (non-managed) deployments where the binary must be on the system PATH.
const INSTALL_HINTS: Record<TunnelProvider, { macos: string; linux: string; docs: string }> = {
  cloudflare: {
    macos: 'brew install cloudflared',
    linux: 'See pkg.cloudflare.com (apt/rpm) or download the cloudflared binary',
    docs: 'https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/',
  },
  ngrok: {
    macos: 'brew install ngrok',
    linux: 'See ngrok.com/download (apt/snap) or download the ngrok binary',
    docs: 'https://ngrok.com/download',
  },
};

// BinarySection shows the agent-binary state for the selected provider:
// a confirmation when present, Install/Update buttons in managed mode
// (Docker), or manual install instructions for local runs.
export function BinarySection({
  provider,
  binary,
  onChanged,
}: {
  provider: TunnelProvider;
  binary?: TunnelBinary;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function run(action: 'install' | 'update') {
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/tunnels/binaries/${provider}/${action}`);
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const managed = binary?.managed ?? false;

  // Installed.
  if (binary?.installed) {
    return (
      <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs">
        <div className="flex items-center justify-between gap-3">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <CheckCircle2 className="size-3.5 text-green-600" />
            Binary installed
            {binary.version && <span className="font-mono">· {binary.version.split('\n')[0]}</span>}
          </span>
          {managed && (
            <Button variant="outline" size="sm" disabled={busy} onClick={() => run('update')}>
              <Download className="mr-1 size-3.5" />
              {busy ? 'Updating…' : 'Update'}
            </Button>
          )}
        </div>
        {err && <p className="mt-1 text-destructive">{err}</p>}
      </div>
    );
  }

  // Not installed, managed → offer Install.
  if (managed) {
    return (
      <Alert>
        <AlertCircle className="size-4" />
        <AlertTitle>Agent binary not installed</AlertTitle>
        <AlertDescription className="space-y-2">
          <p>The {provider} agent isn't downloaded yet. Install it to enable this tunnel.</p>
          <Button size="sm" disabled={busy} onClick={() => run('install')}>
            <Download className="mr-1 size-4" />
            {busy ? 'Installing…' : `Install ${provider}`}
          </Button>
          {err && <p className="text-destructive">{err}</p>}
        </AlertDescription>
      </Alert>
    );
  }

  // Not installed, not managed → manual install instructions (local only).
  const hint = INSTALL_HINTS[provider];
  return (
    <Alert>
      <AlertCircle className="size-4" />
      <AlertTitle>Agent binary not found on this server</AlertTitle>
      <AlertDescription className="space-y-1.5 text-xs">
        <p>
          Install the {provider} agent on the host running cix, then it'll be picked up
          automatically. Only needed for local runs — the Docker images bundle it.
        </p>
        <p>
          <span className="text-muted-foreground">macOS:</span>{' '}
          <code className="font-mono">{hint.macos}</code>
        </p>
        <p>
          <span className="text-muted-foreground">Linux:</span> {hint.linux}
        </p>
        <p>
          <a
            href={hint.docs}
            target="_blank"
            rel="noreferrer"
            className="text-primary underline-offset-2 hover:underline"
          >
            Download docs ↗
          </a>
        </p>
      </AlertDescription>
    </Alert>
  );
}
