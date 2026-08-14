import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import { useForceStopIndex } from '../hooks';

// Hard-aborts an in-flight clone+index for an external project: clears queued
// clone/index jobs and cancels the live index session. Rendered only while the
// project is mid-index; local projects have no server-side pipeline to stop.
export function ForceStopButton({ hash, hostPath }: { hash: string; hostPath: string }) {
  const forceStop = useForceStopIndex();

  async function onClick() {
    try {
      const res = await forceStop.mutateAsync(hash);
      if (res.cancelled || res.jobs_cleared > 0) {
        toast.success('Indexing stopped', {
          description: `${hostPath} — cleared ${res.jobs_cleared} queued job(s)`,
        });
      } else {
        toast.info('Nothing was indexing', { description: hostPath });
      }
    } catch (err) {
      toast.error('Could not stop indexing', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Button
      variant="quietDanger"
      size="sm"
      onClick={onClick}
      disabled={forceStop.isPending}
      title="Abort the running index and clear queued clone/index jobs."
    >
      {forceStop.isPending ? <Dots /> : null}
      Force stop
    </Button>
  );
}
