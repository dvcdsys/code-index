import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { Session } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { TD, TR } from '@/ui/table';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/ui/tooltip';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { useDeleteSession } from '../hooks';

export function SessionRow({ session }: { session: Session }) {
  const del = useDeleteSession();
  const ua = session.last_seen_ua ?? '—';

  async function onSignOut() {
    try {
      await del.mutateAsync(session.id);
      toast.success('Session ended');
    } catch (err) {
      toast.error('Could not end the session', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <TR>
      <TD mono title={formatDateTime(session.created_at)}>
        {formatRelative(session.created_at)}
      </TD>
      <TD mono title={formatDateTime(session.last_seen_at)}>
        {formatRelative(session.last_seen_at)}
      </TD>
      <TD mono>{session.last_seen_ip ?? '—'}</TD>
      <TD className="max-w-[280px]">
        <span className="flex items-center gap-2">
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="min-w-0 truncate font-mono text-[12px] text-dim">{ua}</span>
            </TooltipTrigger>
            <TooltipContent className="break-all">{ua}</TooltipContent>
          </Tooltip>
          {session.is_current ? <Badge variant="ink">this one</Badge> : null}
        </span>
      </TD>
      <TD align="right">
        {session.is_current ? (
          // Ending your own session from a list of rows is a footgun — the
          // sidebar's Sign out is the deliberate path, and the tooltip says so.
          <Tooltip>
            <TooltipTrigger asChild>
              <span>
                <Button variant="ghost" size="sm" disabled>
                  Sign out
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>Use the sidebar Sign out to end this one.</TooltipContent>
          </Tooltip>
        ) : (
          <Button variant="quietDanger" size="sm" onClick={onSignOut} disabled={del.isPending}>
            {del.isPending ? <Dots /> : null}
            Sign out
          </Button>
        )}
      </TD>
    </TR>
  );
}
