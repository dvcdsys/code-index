import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import type { Role } from '@/api/types';
import { useUpdateUser } from '../hooks';

// Inline role edit. The server enforces the last-admin guard — on a 409 we
// surface the toast and the next refetch snaps the value back.
export function UserRoleSelect({
  userId,
  role,
  disabled = false,
}: {
  userId: string;
  role: Role;
  disabled?: boolean;
}) {
  const update = useUpdateUser();

  async function onChange(next: Role) {
    if (next === role) return;
    try {
      await update.mutateAsync({ id: userId, body: { role: next } });
      toast.success('Role updated', { description: `now ${next}` });
    } catch (err) {
      toast.error('Could not update the role', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Select
      value={role}
      onValueChange={(v) => void onChange(v as Role)}
      disabled={disabled || update.isPending}
    >
      <SelectTrigger className="is-sm h-[30px]" aria-label="Role">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="user">user</SelectItem>
        <SelectItem value="admin">admin</SelectItem>
      </SelectContent>
    </Select>
  );
}
