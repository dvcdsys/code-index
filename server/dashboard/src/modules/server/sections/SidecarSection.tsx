import { useState } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';
import { useRestartSidecar, useSidecarStatus } from '../hooks';
import { SidecarStateBadge } from '../components/SidecarStateBadge';

function formatUptime(seconds?: number): string {
  if (!seconds || seconds <= 0) return '—';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

// SidecarSection renders the live llama-server status (PID, uptime, ready,
// model, last error) plus a "Restart sidecar" button. Used when an admin
// changes a runtime-config field that needs the new weights / flags to
// take effect, OR for opportunistic recovery when the sidecar got stuck.
export function SidecarSection() {
  const status = useSidecarStatus();
  const restart = useRestartSidecar();
  const [confirm, setConfirm] = useState(false);

  const data = status.data;
  const restarting = data?.restart_in_flight || data?.state === 'restarting';

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Sidecar
          <SidecarStateBadge state={data?.state} />
        </CardTitle>
        <CardDescription>
          The llama-server child process embedding chunks for this index.
          Restart re-spawns with the latest runtime config.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {data?.last_error ? (
          <Alert variant="destructive">
            <AlertTitle>Sidecar reported an error</AlertTitle>
            <AlertDescription className="font-mono text-xs">{data.last_error}</AlertDescription>
          </Alert>
        ) : null}

        <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-2">
          <Field label="PID" value={data?.pid ? String(data.pid) : '—'} />
          <Field label="Uptime" value={formatUptime(data?.uptime_seconds)} />
          <Field label="Model" value={data?.model ?? '—'} mono />
          <Field label="Queue in flight" value={String(data?.in_flight ?? 0)} />
        </dl>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setConfirm(true)}
            disabled={data?.state === 'disabled' || restarting}
          >
            {restarting ? (
              <Loader2 className="mr-1 h-4 w-4 animate-spin" />
            ) : (
              <RefreshCw className="mr-1 h-4 w-4" />
            )}
            {restarting ? 'Restarting…' : 'Restart sidecar'}
          </Button>
        </div>
      </CardContent>

      <Dialog open={confirm} onOpenChange={(next) => (!restart.isPending ? setConfirm(next) : null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Restart llama-server?</DialogTitle>
            <DialogDescription>
              Drains the embedding queue (up to 30s) and respawns. Active
              indexing batches will fail mid-call and need to be re-driven by
              the operator (<code className="rounded bg-muted px-1 py-0.5 text-xs">cix reindex</code>).
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)} disabled={restart.isPending}>
              Cancel
            </Button>
            <Button
              onClick={async () => {
                try {
                  await restart.mutateAsync();
                  toast.success('Sidecar restart accepted', {
                    description: 'Watch the status badge — it should return to Running within a few seconds.',
                  });
                  setConfirm(false);
                } catch (e) {
                  const detail = e instanceof ApiError ? e.detail : String(e);
                  toast.error('Restart failed', { description: detail });
                }
              }}
              disabled={restart.isPending}
            >
              {restart.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
              Restart
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex justify-between gap-4 border-b border-dashed py-1.5 sm:border-0">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className={mono ? 'truncate font-mono text-xs' : 'text-sm'} title={value}>
        {value}
      </dd>
    </div>
  );
}
