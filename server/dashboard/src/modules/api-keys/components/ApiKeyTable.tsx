import type { ApiKey } from '@/api/types';
import { Badge, StatusDot } from '@/ui/badge';
import { Table, TBody, THead, TR, TH, TD } from '@/ui/table';
import { Button } from '@/ui/button';
import { useCopy } from '@/lib/useCopy';
import { cn } from '@/lib/cn';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { RevokeApiKeyDialog } from './RevokeApiKeyDialog';

interface Props {
  keys: ApiKey[];
  /** Owner column appears in admin "All keys" mode. */
  showOwner?: boolean;
  /** Maps owner_user_id → email for the Owner column. Omit when showOwner is false. */
  ownerEmail?: (id: string) => string | undefined;
  /** Whether the current viewer can revoke a row. The server enforces it too. */
  canRevoke: (key: ApiKey) => boolean;
}

// The status square in the Name cell answers "has this key ever been used?"
// at a glance — green for in-use, faint for never, and a revoked key drops to
// a struck-through name with an outlined chip.
export function ApiKeyTable({ keys, showOwner = false, ownerEmail, canRevoke }: Props) {
  return (
    <Table card>
      <THead>
        <TR>
          <TH>Name</TH>
          <TH>Prefix</TH>
          {showOwner ? <TH>Owner</TH> : null}
          <TH>Created</TH>
          <TH>Last used</TH>
          <TH>Last IP</TH>
          <TH align="right" className="w-[168px]">
            Actions
          </TH>
        </TR>
      </THead>
      <TBody>
        {keys.map((k) => {
          const revoked = Boolean(k.revoked);
          const used = Boolean(k.last_used_at);
          return (
            <TR key={k.id}>
              <TD>
                <span className="flex items-center gap-2.5">
                  <StatusDot tone={revoked ? 'idle' : used ? 'ok' : 'idle'} />
                  <span className={cn('font-semibold', revoked && 'text-faint line-through')}>
                    {k.name}
                  </span>
                  {revoked ? <Badge variant="quiet">revoked</Badge> : null}
                </span>
              </TD>
              <TD mono>{k.prefix}…</TD>
              {showOwner ? (
                <TD mono title={k.owner_user_id}>
                  {ownerEmail?.(k.owner_user_id) ?? `${k.owner_user_id.slice(0, 8)}…`}
                </TD>
              ) : null}
              <TD mono title={formatDateTime(k.created_at)}>
                {formatRelative(k.created_at)}
              </TD>
              <TD
                mono
                title={k.last_used_at ? formatDateTime(k.last_used_at) : undefined}
                className={cn(!used && 'text-faint')}
              >
                {formatRelative(k.last_used_at)}
              </TD>
              <TD mono>{k.last_used_ip ?? '—'}</TD>
              <TD align="right">
                <span className="flex items-center justify-end gap-2">
                  <CopyPrefix prefix={k.prefix} />
                  {revoked || !canRevoke(k) ? null : (
                    <RevokeApiKeyDialog id={k.id} name={k.name} prefix={k.prefix} />
                  )}
                </span>
              </TD>
            </TR>
          );
        })}
      </TBody>
    </Table>
  );
}

// Copies the prefix, not the secret — the secret is unrecoverable after
// creation. Useful for grepping server logs for a specific key.
function CopyPrefix({ prefix }: { prefix: string }) {
  const { copied, copy } = useCopy();
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => void copy(prefix)}
      title="Copy the key prefix (not the secret)"
    >
      {copied ? 'Copied' : 'Copy'}
    </Button>
  );
}
