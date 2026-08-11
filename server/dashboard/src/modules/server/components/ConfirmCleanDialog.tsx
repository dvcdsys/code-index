import type { ReclaimCategory } from '@/api/types';
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
  // Only the categories the admin ticked — the dialog must describe exactly
  // what the confirm button will act on, not everything that was found.
  categories: ReclaimCategory[];
}

export function ConfirmCleanDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending,
  categories,
}: Props) {
  const total = categories.reduce((n, c) => n + c.size_bytes, 0);
  const items = categories.reduce((n, c) => n + c.item_count, 0);
  const destructive = categories.filter((c) => c.destructive);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <span className="cix-dot is-busy" aria-hidden />
          <DialogTitle>Delete {items} items and free {formatBytes(total)}?</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <DialogDescription>
            This cannot be undone. Each item is re-checked against live state immediately before
            it is removed, so anything that came back since the analysis is skipped rather than
            deleted — but what does get deleted is gone.
          </DialogDescription>

          <ul className="m-0 flex list-none flex-col gap-1.5 border p-3 font-mono text-[12.5px]">
            {categories.map((c) => (
              <li key={c.id} className="flex flex-wrap items-baseline gap-2">
                <span className="text-muted">{c.label}</span>
                <span className="ml-auto">{c.item_count} items</span>
                <span className="font-bold">{formatBytes(c.size_bytes)}</span>
              </li>
            ))}
          </ul>

          {destructive.length > 0 ? (
            <Callout variant="warn">
              <b>Re-downloading is expensive</b>
              <p>
                {destructive.map((c) => c.label).join(', ')} cannot be regenerated locally. Getting
                a model file back means a multi-minute download the next time it is selected.
              </p>
            </Callout>
          ) : null}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={isPending}>
            {isPending ? <Dots /> : null}
            Clean {formatBytes(total)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
