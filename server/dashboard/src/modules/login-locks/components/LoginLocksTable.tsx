import type { LoginLock } from '@/api/types';
import { Badge } from '@/ui/badge';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/ui/table';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { ResetLockButton } from './ResetLockButton';

export function LoginLocksTable({ locks }: { locks: LoginLock[] }) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[140px]">Type</TableHead>
            <TableHead>IP</TableHead>
            <TableHead>Email</TableHead>
            <TableHead className="text-right">Attempts</TableHead>
            <TableHead>Frees</TableHead>
            <TableHead className="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {locks.map((l) => (
            <TableRow key={`${l.type}|${l.ip}|${l.email ?? ''}`}>
              <TableCell>
                {l.type === 'ip_email' ? (
                  <Badge variant="secondary" title="Per-account: this email from this IP">
                    IP + email
                  </Badge>
                ) : (
                  <Badge variant="outline" title="Per-IP: many emails from one source">
                    IP only
                  </Badge>
                )}
              </TableCell>
              <TableCell className="font-mono text-xs">{l.ip}</TableCell>
              <TableCell className="font-medium">{l.email || '—'}</TableCell>
              <TableCell className="text-right tabular-nums text-muted-foreground">
                {l.attempts}/{l.limit}
              </TableCell>
              <TableCell
                className="text-xs text-muted-foreground"
                title={formatDateTime(l.expires_at)}
              >
                {formatRelative(l.expires_at)}
              </TableCell>
              <TableCell className="text-right">
                <ResetLockButton lock={l} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
