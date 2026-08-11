import { useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import { Callout } from '@/ui/alert';
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
import { Chip } from '@/ui/code';
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

// The sidecar lives in the right rail, not in the form column: its state has
// to stay visible while the admin edits the fields that will restart it.
// Facts are a key/value block — read-only truth belongs there, never in
// greyed-out inputs.
export function SidecarRail() {
  const status = useSidecarStatus();
  const restart = useRestartSidecar();
  const [confirm, setConfirm] = useState(false);

  const data = status.data;
  const restarting = data?.restart_in_flight || data?.state === 'restarting';

  return (
    <Card>
      <CardHead>
        <SidecarStateBadge state={data?.state} />
      </CardHead>
      <CardBody className="flex flex-col gap-3.5">
        {data?.last_error ? (
          <Callout variant="danger">
            <b>Sidecar reported an error</b>
            <p className="font-mono text-[11.5px]">{data.last_error}</p>
          </Callout>
        ) : null}

        <KV
          rows={[
            { label: 'pid', value: data?.pid ? String(data.pid) : '—' },
            { label: 'uptime', value: formatUptime(data?.uptime_seconds) },
            { label: 'in flight', value: String(data?.in_flight ?? 0) },
            { label: 'model', value: data?.model ?? '—', title: data?.model },
          ]}
        />

        <Button
          className="w-full"
          onClick={() => setConfirm(true)}
          disabled={data?.state === 'disabled' || restarting}
        >
          {restarting ? (
            <>
              <Dots /> Restarting
            </>
          ) : (
            'Restart sidecar'
          )}
        </Button>
      </CardBody>

      <Dialog open={confirm} onOpenChange={(next) => (!restart.isPending ? setConfirm(next) : null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Restart llama-server?</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <DialogDescription>
              The embedding queue drains for up to 30 s, then the child respawns. Indexing batches
              in flight fail mid-call and have to be re-driven with <Chip>cix reindex</Chip>.
            </DialogDescription>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)} disabled={restart.isPending}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={restart.isPending}
              onClick={async () => {
                try {
                  await restart.mutateAsync();
                  toast.success('Restart accepted', {
                    description: 'The state chip returns to RUNNING within a few seconds.',
                  });
                  setConfirm(false);
                } catch (e) {
                  toast.error('Restart failed', {
                    description: e instanceof ApiError ? e.detail : String(e),
                  });
                }
              }}
            >
              {restart.isPending ? <Dots /> : null}
              Restart
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
