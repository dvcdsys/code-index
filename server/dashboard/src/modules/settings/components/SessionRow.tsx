import { Loader2, LogOut } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { Session } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { TableCell, TableRow } from '@/ui/table';
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
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Could not end session', { description: detail });
    }
  }

  return (
    <TableRow>
      <TableCell
        className="text-xs text-muted-foreground"
        title={formatDateTime(session.created_at)}
      >
        {formatRelative(session.created_at)}
      </TableCell>
      <TableCell
        className="text-xs text-muted-foreground"
        title={formatDateTime(session.last_seen_at)}
      >
        {formatRelative(session.last_seen_at)}
      </TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {session.last_seen_ip ?? '—'}
      </TableCell>
      <TableCell className="max-w-[280px] truncate text-xs text-muted-foreground">
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="cursor-help">{ua}</span>
          </TooltipTrigger>
          <TooltipContent className="max-w-md break-all">{ua}</TooltipContent>
        </Tooltip>
      </TableCell>
      <TableCell>
        {session.is_current ? <Badge variant="outline">current</Badge> : null}
      </TableCell>
      <TableCell className="text-right">
        {session.is_current ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <span>
                <Button variant="ghost" size="sm" disabled>
                  <LogOut className="mr-1 h-4 w-4" />
                  Sign out
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>
              This is your current session. Use the sidebar Sign out to end it.
            </TooltipContent>
          </Tooltip>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            onClick={onSignOut}
            disabled={del.isPending}
            className="text-destructive hover:text-destructive"
          >
            {del.isPending ? (
              <Loader2 className="mr-1 h-4 w-4 animate-spin" />
            ) : (
              <LogOut className="mr-1 h-4 w-4" />
            )}
            Sign out
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}
