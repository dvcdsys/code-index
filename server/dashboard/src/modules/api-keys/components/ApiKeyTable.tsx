import type { ApiKey } from '@/api/types';
import { Badge } from '@/ui/badge';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/ui/table';
import { cn } from '@/lib/cn';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { RevokeApiKeyDialog } from './RevokeApiKeyDialog';

interface Props {
  keys: ApiKey[];
  /** Owner column appears in admin "All keys" mode. */
  showOwner?: boolean;
  /** Maps owner_user_id → email for the Owner column. Omit when showOwner is false. */
  ownerEmail?: (id: string) => string | undefined;
  /** Whether the current viewer can revoke a row. Server enforces too. */
  canRevoke: (key: ApiKey) => boolean;
}

export function ApiKeyTable({ keys, showOwner = false, ownerEmail, canRevoke }: Props) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Prefix</TableHead>
            {showOwner ? <TableHead>Owner</TableHead> : null}
            <TableHead>Created</TableHead>
            <TableHead>Last used</TableHead>
            <TableHead>Last IP</TableHead>
            <TableHead className="w-24 text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {keys.map((k) => {
            const revoked = Boolean(k.revoked);
            return (
              <TableRow
                key={k.id}
                className={cn(revoked && 'opacity-60')}
              >
                <TableCell className={cn('font-medium', revoked && 'line-through')}>
                  <div className="flex items-center gap-2">
                    {k.name}
                    {revoked ? <Badge variant="secondary">revoked</Badge> : null}
                  </div>
                </TableCell>
                <TableCell className="font-mono text-xs">{k.prefix}…</TableCell>
                {showOwner ? (
                  <TableCell className="text-xs text-muted-foreground">
                    {ownerEmail?.(k.owner_user_id) ?? k.owner_user_id.slice(0, 8)}
                  </TableCell>
                ) : null}
                <TableCell
                  className="text-xs text-muted-foreground"
                  title={formatDateTime(k.created_at)}
                >
                  {formatRelative(k.created_at)}
                </TableCell>
                <TableCell
                  className="text-xs text-muted-foreground"
                  title={k.last_used_at ? formatDateTime(k.last_used_at) : undefined}
                >
                  {formatRelative(k.last_used_at)}
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {k.last_used_ip ?? '—'}
                </TableCell>
                <TableCell className="text-right">
                  {revoked || !canRevoke(k) ? null : (
                    <RevokeApiKeyDialog id={k.id} name={k.name} prefix={k.prefix} />
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
