import { Card, CardBody, CardHead } from '@/ui/card';
import { Progress } from '@/ui/progress';
import { Status } from '@/ui/badge';
import { useIndexStatus } from '../hooks';

// Live progress for an external project mid-run: a discrete cell bar (or a
// bare count when this is a first full index and the total is unknown until
// the directory walk finishes), elapsed time, and the files currently being
// read. Mounting is gated by the caller on (isExternal && indexing); the hook
// polls every 1.5 s and stops the moment `enabled` goes false.
export function IndexingProgressCard({ hash }: { hash: string }) {
  const status = useIndexStatus(hash, true);
  const progress = status.data?.progress;

  const processed = progress?.files_processed ?? 0;
  const total = progress?.files_total ?? 0;
  const hasTotal = total > 0;
  const elapsed = progress?.elapsed_seconds;
  const currentFiles = progress?.current_files ?? [];

  return (
    <Card>
      <CardHead>
        <Status tone="warn" className="font-mono text-[11.5px] uppercase tracking-[0.14em]">
          indexing
        </Status>
        <span className="ml-auto font-mono text-[12px] font-normal text-muted">
          {hasTotal ? `${processed} / ${total} files` : `${processed} files`}
          {typeof elapsed === 'number' ? ` · ${Math.round(elapsed)}s` : ''}
        </span>
      </CardHead>
      <CardBody className="flex flex-col gap-3.5">
        {hasTotal ? (
          <Progress
            value={processed / total}
            cells={16}
            label={`${processed} of ${total} files indexed`}
          />
        ) : null}

        {currentFiles.length > 0 ? (
          <div>
            <span className="cix-label">Currently reading</span>
            <ul className="mt-1.5 flex list-none flex-col gap-0.5 p-0 font-mono text-[12px]">
              {currentFiles.map((f, i) => (
                <li key={`${i}-${f}`} className={i === 0 ? 'break-all' : 'break-all text-muted'}>
                  {f}
                </li>
              ))}
            </ul>
          </div>
        ) : (
          <p className="m-0 text-[13.5px] text-dim">
            Stats and search results stay incomplete until the run finishes — this card updates
            live.
          </p>
        )}
      </CardBody>
    </Card>
  );
}
