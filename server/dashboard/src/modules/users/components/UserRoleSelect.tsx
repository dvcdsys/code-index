import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import type { Role } from '@/api/types';
import { useUpdateUser } from '../hooks';

// Inline role-edit. The server enforces the last-admin guard — if it returns
// 409 we just surface the toast and the next refetch resets the value.
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
      toast.success('Role updated', { description: `Now ${next}` });
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Could not update role', { description: detail });
    }
  }

  return (
    <Select
      value={role}
      onValueChange={(v) => void onChange(v as Role)}
      disabled={disabled || update.isPending}
    >
      <SelectTrigger className="h-8 w-[110px]">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="viewer">Viewer</SelectItem>
        <SelectItem value="admin">Admin</SelectItem>
      </SelectContent>
    </Select>
  );
}
