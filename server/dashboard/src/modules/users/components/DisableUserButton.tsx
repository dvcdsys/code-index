import { Loader2, ShieldOff, ShieldCheck } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Button } from '@/ui/button';
import { useUpdateUser } from '../hooks';

// One-click toggle. Server's last-admin guard kicks in for the disable path.
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
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error(disabled ? 'Could not enable user' : 'Could not disable user', {
        description: detail,
      });
    }
  }

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={onToggle}
      disabled={update.isPending}
      title={disabled ? 'Re-enable account' : 'Disable account (cannot log in)'}
    >
      {update.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : disabled ? (
        <ShieldCheck className="mr-1 h-4 w-4" />
      ) : (
        <ShieldOff className="mr-1 h-4 w-4" />
      )}
      {disabled ? 'Enable' : 'Disable'}
    </Button>
  );
}
