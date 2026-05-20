import { Loader2, OctagonX } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useForceStopIndex } from '../hooks';

// ForceStopButton hard-aborts an in-flight clone+index for an external
// project: clears queued clone/index jobs and cancels the live index session.
// Rendered only while the project is mid-index. External projects only —
// local projects have no server-side pipeline to stop.
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
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to stop indexing', { description: detail });
    }
  }

  return (
    <Button
      variant="destructive"
      size="sm"
      onClick={onClick}
      disabled={forceStop.isPending}
      title="Force stop — abort the running index and clear queued clone/index jobs."
    >
      {forceStop.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <OctagonX className="mr-1 h-4 w-4" />
      )}
      Force stop
    </Button>
  );
}
