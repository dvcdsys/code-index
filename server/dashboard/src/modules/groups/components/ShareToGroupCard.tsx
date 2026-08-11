import type { Group } from '@/api/types';
import { Button } from '@/ui/button';
import { Card, CardHead } from '@/ui/card';

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

// Shared by the project detail page (admin, external projects) and the
// workspace detail page (owner shares to their own groups, admin to any).
// Purely presentational — callers wire the mutations and supply the list.
export function ShareToGroupCard({
  heading,
  description,
  groups,
  sharedIds,
  onShare,
  onUnshare,
  busy,
  emptyText = 'No view groups available.',
}: Props) {
  const shared = new Set(sharedIds);
  return (
    <Card>
      <CardHead title={heading} />
      {groups.length === 0 ? (
        <p className="m-0 px-[18px] py-4 text-sm text-dim">{emptyText}</p>
      ) : (
        <>
          <p className="m-0 border-b border-line-soft px-[18px] py-3 text-[13px] text-dim">
            {description}
          </p>
          {groups.map((g) => {
            const isShared = shared.has(g.id);
            return (
              <div key={g.id} className="cix-row">
                <span className={`cix-dot ${isShared ? 'is-ok' : ''}`} aria-hidden />
                <div className="min-w-0 flex-1">
                  <div className="cix-row__title truncate">{g.name}</div>
                  {g.description ? (
                    <div className="cix-row__meta truncate">{g.description}</div>
                  ) : null}
                </div>
                <Button
                  variant={isShared ? 'default' : 'primary'}
                  size="sm"
                  disabled={busy}
                  onClick={() => (isShared ? onUnshare(g.id) : onShare(g.id))}
                >
                  {isShared ? 'Unshare' : 'Share'}
                </Button>
              </div>
            );
          })}
        </>
      )}
    </Card>
  );
}
