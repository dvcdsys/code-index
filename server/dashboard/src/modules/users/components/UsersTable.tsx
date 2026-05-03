import type { UserWithStats } from '@/api/types';
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
import { DeleteUserDialog } from './DeleteUserDialog';
import { DisableUserButton } from './DisableUserButton';
import { UserRoleSelect } from './UserRoleSelect';

export function UsersTable({
  users,
  currentUserId,
}: {
  users: UserWithStats[];
  currentUserId: string | undefined;
}) {
  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Email</TableHead>
            <TableHead className="w-[140px]">Role</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>Last login</TableHead>
            <TableHead className="text-right">Sessions</TableHead>
            <TableHead className="text-right">API keys</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {users.map((u) => {
            const isSelf = u.id === currentUserId;
            return (
              <TableRow key={u.id} className={cn(u.disabled && 'opacity-60')}>
                <TableCell className="font-medium">
                  <div className="flex items-center gap-2">
                    {u.email}
                    {isSelf ? <Badge variant="outline">You</Badge> : null}
                  </div>
                </TableCell>
                <TableCell>
                  <UserRoleSelect userId={u.id} role={u.role} disabled={isSelf} />
                </TableCell>
                <TableCell
                  className="text-xs text-muted-foreground"
                  title={formatDateTime(u.created_at)}
                >
                  {formatRelative(u.created_at)}
                </TableCell>
                <TableCell
                  className="text-xs text-muted-foreground"
                  title={u.last_login_at ? formatDateTime(u.last_login_at) : undefined}
                >
                  {formatRelative(u.last_login_at)}
                </TableCell>
                <TableCell className="text-right tabular-nums text-muted-foreground">
                  {u.active_sessions_count}
                </TableCell>
                <TableCell className="text-right tabular-nums text-muted-foreground">
                  {u.api_keys_count}
                </TableCell>
                <TableCell>
                  {u.disabled ? (
                    <Badge variant="secondary">disabled</Badge>
                  ) : (
                    <Badge variant="outline">active</Badge>
                  )}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-1">
                    {isSelf ? null : (
                      <>
                        <DisableUserButton userId={u.id} disabled={u.disabled} />
                        <DeleteUserDialog userId={u.id} email={u.email} />
                      </>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
