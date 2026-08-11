import { ApiError } from '@/api/client';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Empty } from '@/ui/card';
import { Page } from '@/ui/page';
import { SkeletonRows } from '@/ui/skeleton';
import { TableNote } from '@/ui/table';
import { LoginLocksTable } from './components/LoginLocksTable';
import { useLoginLocks } from './hooks';

export default function LoginLocksPage() {
  const { data, error, isLoading } = useLoginLocks();
  const count = data?.locks.length ?? 0;

  useStatusFact(data ? `${count} active lock${count === 1 ? '' : 's'}` : null);

  return (
    <Page
      title="Login security"
      subtitle="Accounts and source IPs the login rate limiter is currently blocking after too many failed attempts. Locks expire on their own — reset one to let someone back in immediately."
    >
      {isLoading ? (
        <SkeletonRows rows={3} />
      ) : error ? (
        <Callout variant="danger">
          <b>Could not load login locks</b>
          <p>{error instanceof ApiError ? error.detail : String(error)}</p>
        </Callout>
      ) : !data || data.locks.length === 0 ? (
        <Empty title="No active locks">
          Nobody is rate-limited right now. Locks appear here the moment an account or an IP
          crosses the failed-login threshold.
        </Empty>
      ) : (
        <>
          <LoginLocksTable locks={data.locks} />
          <TableNote
            left={`${count} lock${count === 1 ? '' : 's'}`}
            right="locks clear themselves when their window expires"
          />
        </>
      )}
    </Page>
  );
}
