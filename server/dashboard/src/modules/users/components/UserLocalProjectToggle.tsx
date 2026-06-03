import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Switch } from '@/ui/switch';
import { useUpdateUser } from '../hooks';

// Inline toggle for the per-user "can create local projects / index" permission.
// The stored flag is `local_project_disabled` (true = forbidden); the switch is
// presented in the positive sense ("Allowed") so it reads naturally, so checked
// === !local_project_disabled. The server is the source of truth — on error we
// surface a toast and the next refetch resets the control.
export function UserLocalProjectToggle({
  userId,
  localProjectDisabled,
  disabled = false,
}: {
  userId: string;
  localProjectDisabled: boolean;
  disabled?: boolean;
}) {
  const update = useUpdateUser();
  const allowed = !localProjectDisabled;

  async function onChange(nextAllowed: boolean) {
    const nextDisabled = !nextAllowed;
    if (nextDisabled === localProjectDisabled) return;
    try {
      await update.mutateAsync({
        id: userId,
        body: { local_project_disabled: nextDisabled },
      });
      toast.success('Permission updated', {
        description: nextDisabled
          ? 'Local project creation & indexing disabled'
          : 'Local project creation & indexing enabled',
      });
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Could not update permission', { description: detail });
    }
  }

  return (
    <Switch
      checked={allowed}
      onCheckedChange={(v) => void onChange(v)}
      disabled={disabled || update.isPending}
      aria-label="Allow creating local projects and indexing"
    />
  );
}
