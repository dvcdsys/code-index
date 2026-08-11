import { Button, Dots } from '@/ui/button';
import { Chip } from '@/ui/code';
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
  // Per-field summary so the admin sees exactly what is about to be applied
  // before the sidecar gets killed.
  changes: Array<{ field: string; from: string; to: string }>;
}

// Saving runtime config restarts the embedding sidecar and fails whatever
// indexing batches are mid-call — not a quiet operation, so it is always
// confirmed, with the diff spelled out.
export function SaveAndRestartDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending,
  changes,
}: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Apply config and restart the sidecar?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            The overrides are written to the database, then the embedding queue drains for up to
            30 s and llama-server restarts. Batches in flight at that moment fail — re-run{' '}
            <Chip>cix reindex</Chip> for the affected projects.
          </DialogDescription>

          {changes.length > 0 ? (
            <ul className="m-0 flex list-none flex-col gap-1.5 border p-3 font-mono text-[12.5px]">
              {changes.map((c) => (
                <li key={c.field} className="flex flex-wrap items-baseline gap-2">
                  <span className="text-muted">{c.field}</span>
                  <span className="text-faint line-through">{c.from}</span>
                  <span aria-hidden>→</span>
                  <span className="font-bold">{c.to}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="cix-hint m-0">no field changes — restart only</p>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={isPending}>
            {isPending ? <Dots /> : null}
            Save &amp; restart
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
