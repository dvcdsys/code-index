import type { DatabaseState } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';

interface Props {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  onConfirm: () => void;
  isPending: boolean;
  state: DatabaseState;
  // True when the admin arrived here from the incremental-reclaim toggle
  // rather than from the compact button. Same operation, different reason,
  // and the dialog has to say which — a toggle that silently costs a restart
  // is the single most surprising thing in this feature.
  viaToggle: boolean;
}

function humanDuration(seconds: number): string {
  if (seconds < 90) return `about ${Math.max(1, Math.round(seconds))} seconds`;
  return `about ${Math.round(seconds / 60)} minutes`;
}

export function ConfirmCompactDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending,
  state,
  viaToggle,
}: Props) {
  const estimate = humanDuration(state.estimated_seconds);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <span className="cix-dot is-busy" aria-hidden />
          <DialogTitle>
            {viaToggle
              ? 'Switching to incremental reclaim rebuilds the database'
              : `Compact the database and reclaim ${formatBytes(state.reclaimable_bytes, { zero: '0 B' })}?`}
          </DialogTitle>
        </DialogHeader>
        <DialogBody>
          {viaToggle ? (
            <DialogDescription>
              SQLite can only change a populated database&rsquo;s reclaim mode by rebuilding it,
              so turning this on costs exactly one compaction. It is the same operation as the
              Compact button, with the same interruption.
            </DialogDescription>
          ) : (
            <DialogDescription>
              The database is rebuilt into a fresh file and the old one is replaced.{' '}
              {formatBytes(state.reclaimable_bytes, { zero: '0 B' })} of the current{' '}
              {formatBytes(state.file_bytes)} is empty space that goes back to the filesystem.
            </DialogDescription>
          )}

          <ul className="m-0 flex list-none flex-col gap-1.5 border p-3 text-[12.5px]">
            <li>
              <b>For {estimate} the server is read-only, not down.</b> Search, browsing and every
              read keep working. Indexing pauses, and changes — including new logins — are
              refused with a &ldquo;try again&rdquo; response.
            </li>
            <li>
              <b>Then it restarts itself</b> to adopt the rebuilt file, and is unavailable until
              it has finished starting up.
            </li>
            <li>
              You stay signed in. Sessions simply stop being extended during the window, which is
              invisible against their two-week lifetime.
            </li>
          </ul>

          <Callout variant="warn">
            Nothing is deleted and nothing is lost: writes are refused rather than dropped, and if
            the machine loses power mid-operation the next start puts the database back to a
            consistent state on its own.
          </Callout>

          {state.free_disk_bytes > 0 && state.free_disk_bytes < state.required_disk_bytes * 2 ? (
            <Callout variant="warn">
              The copy needs about {formatBytes(state.required_disk_bytes)} alongside the original
              and there is {formatBytes(state.free_disk_bytes)} free.
            </Callout>
          ) : null}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={isPending}>
            {isPending ? <Dots /> : null}
            {viaToggle ? 'Rebuild and switch' : 'Compact now'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
