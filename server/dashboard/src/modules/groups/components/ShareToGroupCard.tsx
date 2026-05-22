import type { Group } from '@/api/types';
import { Button } from '@/ui/button';
import { Card, CardContent } from '@/ui/card';

interface Props {
  heading: string;
  description: string;
  /** Groups the caller may share to. */
  groups: Group[];
  /** IDs currently shared. */
  sharedIds: string[];
  onShare: (groupId: string) => void;
  onUnshare: (groupId: string) => void;
  busy?: boolean;
  emptyText?: string;
}

// Reused by the project detail (admin, external projects) and workspace detail
// (owner shares to their own groups; admin to any). Pure presentational —
// callers wire the mutations + supply the candidate group list.
export function ShareToGroupCard({
  heading,
  description,
  groups,
  sharedIds,
  onShare,
  onUnshare,
  busy,
  emptyText = 'No view-groups available.',
}: Props) {
  const shared = new Set(sharedIds);
  return (
    <section>
      <h2 className="mb-1 text-sm font-medium text-muted-foreground">{heading}</h2>
      <p className="mb-3 text-xs text-muted-foreground">{description}</p>
      {groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">{emptyText}</p>
      ) : (
        <Card>
          <CardContent className="divide-y p-0">
            {groups.map((g) => {
              const isShared = shared.has(g.id);
              return (
                <div key={g.id} className="flex items-center justify-between gap-2 px-4 py-2.5">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{g.name}</div>
                    {g.description ? (
                      <div className="truncate text-xs text-muted-foreground">{g.description}</div>
                    ) : null}
                  </div>
                  <Button
                    variant={isShared ? 'outline' : 'default'}
                    size="sm"
                    disabled={busy}
                    onClick={() => (isShared ? onUnshare(g.id) : onShare(g.id))}
                  >
                    {isShared ? 'Unshare' : 'Share'}
                  </Button>
                </div>
              );
            })}
          </CardContent>
        </Card>
      )}
    </section>
  );
}
