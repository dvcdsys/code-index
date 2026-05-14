import { Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useReindexProject } from '../hooks';

export function ReindexProjectButton({ hash, hostPath }: { hash: string; hostPath: string }) {
  const reindex = useReindexProject();

  async function onClick() {
    try {
      const res = await reindex.mutateAsync(hash);
      if (res.status === 'already_running') {
        toast.info('Reindex already running', { description: hostPath });
      } else {
        toast.success('Reindex enqueued', { description: hostPath });
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to enqueue reindex', { description: detail });
    }
  }

  return (
    <Button variant="outline" size="sm" onClick={onClick} disabled={reindex.isPending}>
      {reindex.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <RefreshCw className="mr-1 h-4 w-4" />
      )}
      Reindex
    </Button>
  );
}
