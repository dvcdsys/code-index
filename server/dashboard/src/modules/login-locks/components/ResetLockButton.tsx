import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { LoginLock } from '@/api/types';
import { Button, Dots } from '@/ui/button';
import { useResetLoginLock } from '../hooks';

// The lock carries its own exact key (type + ip + optional email), so this
// clears precisely that counter — never a scan across keys.
export function ResetLockButton({ lock }: { lock: LoginLock }) {
  const reset = useResetLoginLock();

  async function onReset() {
    try {
      await reset.mutateAsync({ type: lock.type, ip: lock.ip, email: lock.email });
      toast.success('Lock cleared', {
        description: lock.type === 'ip_email' ? `${lock.email} from ${lock.ip}` : `IP ${lock.ip}`,
      });
    } catch (err) {
      toast.error('Could not clear the lock', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  return (
    <Button
      size="sm"
      onClick={onReset}
      disabled={reset.isPending}
      title="Clear this lock now — the user can sign in again immediately"
    >
      {reset.isPending ? <Dots /> : null}
      Reset
    </Button>
  );
}
