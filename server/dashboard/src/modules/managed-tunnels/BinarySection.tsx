import { useState } from 'react';
import { api } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Status } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { Chip } from '@/ui/code';
import type { TunnelBinary, TunnelProvider } from './types';

// Manual install instructions per provider, shown only for local
// (non-managed) deployments where the binary has to be on the system PATH.
const INSTALL_HINTS: Record<TunnelProvider, { macos: string; linux: string; docs: string }> = {
  cloudflare: {
    macos: 'brew install cloudflared',
    linux: 'pkg.cloudflare.com (apt/rpm), or download the cloudflared binary',
    docs: 'https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/',
  },
  ngrok: {
    macos: 'brew install ngrok',
    linux: 'ngrok.com/download (apt/snap), or download the ngrok binary',
    docs: 'https://ngrok.com/download',
  },
};

// The agent binary for the selected provider: a confirmation line when it is
// present, Install/Update in managed mode (Docker), or manual instructions
// for a local run.
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

  if (binary?.installed) {
    return (
      <div className="border px-3.5 py-2.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Status tone="ok" className="font-mono text-[11.5px]">
            binary installed
            {binary.version ? ` · ${binary.version.split('\n')[0]}` : ''}
          </Status>
          {managed ? (
            <Button size="sm" disabled={busy} onClick={() => run('update')}>
              {busy ? <Dots /> : null}
              Update
            </Button>
          ) : null}
        </div>
        {err ? <p className="cix-error mt-1.5">{err}</p> : null}
      </div>
    );
  }

  if (managed) {
    return (
      <Callout variant="warn">
        <b>Agent binary not installed</b>
        <p>The {provider} agent has not been downloaded yet. Install it to enable this tunnel.</p>
        <div className="mt-1 flex items-center gap-3">
          <Button variant="primary" size="sm" disabled={busy} onClick={() => run('install')}>
            {busy ? <Dots /> : null}
            Install {provider}
          </Button>
          {err ? <span className="cix-error">{err}</span> : null}
        </div>
      </Callout>
    );
  }

  const hint = INSTALL_HINTS[provider];
  return (
    <Callout variant="warn">
      <b>Agent binary not found on this server</b>
      <p>
        Install the {provider} agent on the host running cix and it gets picked up automatically.
        Only local runs need this — the Docker images bundle it.
      </p>
      <dl className="cix-kv mt-1">
        <dt>macos</dt>
        <dd className="text-left">
          <Chip>{hint.macos}</Chip>
        </dd>
        <dt>linux</dt>
        <dd className="text-left">{hint.linux}</dd>
      </dl>
      <a
        href={hint.docs}
        target="_blank"
        rel="noreferrer"
        className="mt-1 self-start font-mono text-[11.5px] text-accent hover:underline"
      >
        download docs ↗
      </a>
    </Callout>
  );
}
