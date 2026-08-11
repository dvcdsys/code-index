import type { LoginLock } from '@/api/types';
import { Badge, StatusDot } from '@/ui/badge';
import { Table, TBody, THead, TR, TH, TD } from '@/ui/table';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { ResetLockButton } from './ResetLockButton';

export function LoginLocksTable({ locks }: { locks: LoginLock[] }) {
  return (
    <Table card>
      <THead>
        <TR>
          <TH className="w-[130px]">Scope</TH>
          <TH>IP</TH>
          <TH>Email</TH>
          <TH align="right">Attempts</TH>
          <TH>Frees</TH>
          <TH align="right">Actions</TH>
        </TR>
      </THead>
      <TBody>
        {locks.map((l) => (
          <TR key={`${l.type}|${l.ip}|${l.email ?? ''}`}>
            <TD>
              {l.type === 'ip_email' ? (
                <Badge variant="outline" title="Per-account: this email from this IP">
                  ip + email
                </Badge>
              ) : (
                <Badge variant="warn" title="Per-IP: many emails from one source">
                  ip only
                </Badge>
              )}
            </TD>
            <TD mono>{l.ip}</TD>
            <TD>
              <span className="flex items-center gap-2.5">
                <StatusDot tone="busy" />
                <span className="font-semibold">{l.email || '—'}</span>
              </span>
            </TD>
            <TD mono align="right" className="tabular-nums">
              {l.attempts}/{l.limit}
            </TD>
            <TD mono title={formatDateTime(l.expires_at)}>
              {formatRelative(l.expires_at)}
            </TD>
            <TD align="right">
              <ResetLockButton lock={l} />
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
