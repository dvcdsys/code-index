import { Loader2, Unlock } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { LoginLock } from '@/api/types';
import { Button } from '@/ui/button';
import { useResetLoginLock } from '../hooks';

// One-click unlock for a single row. The lock carries its own exact key
// (type + ip + optional email), so we clear precisely that counter — never a
// scan across keys.
export function ResetLockButton({ lock }: { lock: LoginLock }) {
  const reset = useResetLoginLock();

  async function onReset() {
    try {
      await reset.mutateAsync({
        type: lock.type,
        ip: lock.ip,
        email: lock.email,
      });
      toast.success('Login lock cleared', {
        description:
          lock.type === 'ip_email'
            ? `${lock.email} from ${lock.ip}`
            : `IP ${lock.ip}`,
      });
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Could not clear lock', { description: detail });
    }
  }

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={onReset}
      disabled={reset.isPending}
      title="Clear this lock now (lets the user log in again immediately)"
    >
      {reset.isPending ? (
        <Loader2 className="mr-1 h-4 w-4 animate-spin" />
      ) : (
        <Unlock className="mr-1 h-4 w-4" />
      )}
      Reset
    </Button>
  );
}
