import type { UserWithStats } from '@/api/types';
import { Badge, StatusDot } from '@/ui/badge';
import { Table, TBody, THead, TR, TH, TD } from '@/ui/table';
import { cn } from '@/lib/cn';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { DeleteUserDialog } from './DeleteUserDialog';
import { DisableUserButton } from './DisableUserButton';
import { ResetPasswordDialog } from './ResetPasswordDialog';
import { UserRoleSelect } from './UserRoleSelect';
import { UserLocalProjectToggle } from './UserLocalProjectToggle';

export function UsersTable({
  users,
  currentUserId,
}: {
  users: UserWithStats[];
  currentUserId: string | undefined;
}) {
  return (
    <Table card>
      <THead>
        <TR>
          <TH>Email</TH>
          <TH className="w-[130px]">Role</TH>
          <TH
            className="w-[120px]"
            title="Allow creating local projects and (re)indexing. Search and workspaces are always allowed; admins always are."
          >
            Local projects
          </TH>
          <TH>Created</TH>
          <TH>Last login</TH>
          <TH align="right">Sessions</TH>
          <TH align="right">API keys</TH>
          <TH align="right">Actions</TH>
        </TR>
      </THead>
      <TBody>
        {users.map((u) => {
          const isSelf = u.id === currentUserId;
          return (
            <TR key={u.id}>
              <TD>
                <span className="flex items-center gap-2.5">
                  {/* The square is the account state — disabled accounts keep
                      their row legible instead of being dimmed into noise. */}
                  <StatusDot tone={u.disabled ? 'idle' : 'ok'} />
                  <span className={cn('font-semibold', u.disabled && 'text-faint line-through')}>
                    {u.email}
                  </span>
                  {isSelf ? <Badge variant="outline">you</Badge> : null}
                  {u.disabled ? <Badge variant="quiet">disabled</Badge> : null}
                </span>
              </TD>
              <TD>
                <UserRoleSelect userId={u.id} role={u.role} disabled={isSelf} />
              </TD>
              <TD>
                {/* Admins ignore local_project_disabled entirely, so they get
                    a word instead of a toggle. The stored flag is left alone —
                    it applies again if the admin is later demoted. */}
                {u.role === 'admin' ? (
                  <span className="cix-hint">always</span>
                ) : (
                  <UserLocalProjectToggle
                    userId={u.id}
                    localProjectDisabled={u.local_project_disabled}
                  />
                )}
              </TD>
              <TD mono title={formatDateTime(u.created_at)}>
                {formatRelative(u.created_at)}
              </TD>
              <TD
                mono
                title={u.last_login_at ? formatDateTime(u.last_login_at) : undefined}
                className={cn(!u.last_login_at && 'text-faint')}
              >
                {formatRelative(u.last_login_at)}
              </TD>
              <TD mono align="right" className="tabular-nums">
                {u.active_sessions_count}
              </TD>
              <TD mono align="right" className="tabular-nums">
                {u.api_keys_count}
              </TD>
              <TD align="right">
                <span className="flex items-center justify-end gap-1.5">
                  {isSelf ? (
                    <span className="cix-hint">—</span>
                  ) : (
                    <>
                      <ResetPasswordDialog userId={u.id} email={u.email} />
                      <DisableUserButton userId={u.id} disabled={u.disabled} />
                      <DeleteUserDialog userId={u.id} email={u.email} />
                    </>
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
