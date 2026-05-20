import { Hammer, Loader2 } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useReindexProject } from '../hooks';

// ReindexProjectButton triggers a FULL rebuild — the server clears
// indexed_sha first, so the next job re-embeds every file (wipes prior
// chunks/symbols/refs). This is the heavy "rebuild from scratch" path;
// for the cheap "pull + incremental" path use the Sync button instead.
export function ReindexProjectButton({ hash, hostPath }: { hash: string; hostPath: string }) {
  const reindex = useReindexProject();

  async function onClick() {
    try {
      const res = await reindex.mutateAsync({ hash, full: true });
      if (res.status === 'already_running') {
        toast.info('Reindex already running', { description: hostPath });
      } else {
        toast.success('Full reindex enqueued', { description: `${hostPath} — full rebuild` });
      }
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to enqueue reindex', { description: detail });
    }
  }

  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      disabled={reindex.isPending}
      title="Full reindex — clears the index and re-embeds every file. For a quick pull + incremental update, use Sync."
    >
      {reindex.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <Hammer className="mr-1 h-4 w-4" />
      )}
      Reindex
    </Button>
  );
}
