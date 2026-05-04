import { Loader2 } from 'lucide-react';
import { Button } from '@/ui/button';
import {
  Dialog,
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
  // Per-field summary the dialog renders so the admin sees exactly what
  // they're about to apply before the sidecar gets killed.
  changes: Array<{ field: string; from: string; to: string }>;
}

// SaveAndRestartDialog confirms a runtime-config save that will trigger an
// embedding-sidecar restart. Active indexing batches will fail mid-call —
// not a quiet operation — so we always require explicit confirm before the
// PUT + restart cascade fires.
export function SaveAndRestartDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending,
  changes,
}: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Apply config and restart sidecar?</DialogTitle>
          <DialogDescription>
            Saving will write the overrides to the database, then drain the
            embedding queue (up to 30s) and restart llama-server. Indexing
            batches in flight at restart time will fail — re-run{' '}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">cix reindex</code>{' '}
            for affected projects.
          </DialogDescription>
        </DialogHeader>
        {changes.length > 0 ? (
          <ul className="space-y-1 rounded-md border bg-muted/30 p-3 text-xs">
            {changes.map((c) => (
              <li key={c.field} className="font-mono">
                <span className="text-muted-foreground">{c.field}: </span>
                <span className="line-through text-muted-foreground">{c.from}</span>
                <span className="mx-1">→</span>
                <span>{c.to}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-xs text-muted-foreground">No field changes — restart only.</p>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
          <Button onClick={onConfirm} disabled={isPending}>
            {isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
            Save &amp; Restart
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
