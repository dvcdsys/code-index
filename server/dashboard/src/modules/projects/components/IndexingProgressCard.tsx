import { Loader2 } from 'lucide-react';
import { Alert, AlertTitle } from '@/ui/alert';
import { useIndexStatus } from '../hooks';

// IndexingProgressCard renders live indexing progress for an external project
// while a run is active: a files_processed / files_total bar (or a bare
// "N files indexed" count for a first full index, where the total is unknown
// until the directory walk finishes), elapsed time, and the last few files
// being indexed. Mounting is gated by the caller on (isExternal && indexing);
// the hook polls every 1.5s and stops as soon as `enabled` goes false.
export function IndexingProgressCard({ hash }: { hash: string }) {
  const status = useIndexStatus(hash, true);
  const progress = status.data?.progress;

  const processed = progress?.files_processed ?? 0;
  const total = progress?.files_total ?? 0;
  const hasTotal = total > 0;
  const pct = hasTotal ? Math.min(100, Math.round((processed / total) * 100)) : 0;
  const elapsed = progress?.elapsed_seconds;
  const currentFiles = progress?.current_files ?? [];

  return (
    <Alert>
      <Loader2 className="h-4 w-4 animate-spin" />
      <AlertTitle>Indexing in progress</AlertTitle>
      <div className="mt-2 space-y-3 text-sm">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="font-medium">
            {hasTotal ? `${processed} / ${total} files` : `${processed} files indexed`}
          </span>
          {typeof elapsed === 'number' ? (
            <span className="text-muted-foreground">{Math.round(elapsed)}s elapsed</span>
          ) : null}
        </div>

        {hasTotal ? (
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
            <div
              className="h-full rounded-full bg-primary transition-all duration-500"
              style={{ width: `${pct}%` }}
            />
          </div>
        ) : null}

        {currentFiles.length > 0 ? (
          <div className="space-y-1">
            <div className="text-xs uppercase tracking-wide text-muted-foreground">
              Currently indexing
            </div>
            <ul className="space-y-0.5 font-mono text-xs">
              {currentFiles.map((f, i) => (
                <li
                  key={f}
                  className={i === 0 ? 'break-all text-foreground' : 'break-all text-muted-foreground'}
                >
                  {f}
                </li>
              ))}
            </ul>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            Stats and search results stay incomplete until the run finishes — this page
            updates live.
          </p>
        )}
      </div>
    </Alert>
  );
}
