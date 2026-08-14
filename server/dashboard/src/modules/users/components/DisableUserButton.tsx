import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button, Dots } from '@/ui/button';
import { useUpdateUser } from '../hooks';

// One click each way. The server's last-admin guard covers the disable path.
export function DisableUserButton({
  userId,
  disabled,
}: {
  userId: string;
  /** Current disabled state on the user record. */
  disabled: boolean;
}) {
  const update = useUpdateUser();

  async function onToggle() {
    try {
      await update.mutateAsync({ id: userId, body: { disabled: !disabled } });
      toast.success(disabled ? 'User re-enabled' : 'User disabled');
    } catch (err) {
      toast.error(disabled ? 'Could not enable the user' : 'Could not disable the user', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={onToggle}
      disabled={update.isPending}
      title={disabled ? 'Re-enable this account' : 'Disable this account — no sign-in'}
    >
      {update.isPending ? <Dots /> : null}
      {disabled ? 'Enable' : 'Disable'}
    </Button>
  );
}
