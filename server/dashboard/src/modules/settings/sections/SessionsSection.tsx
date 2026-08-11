import { ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Card, CardBody, CardHead } from '@/ui/card';
import { SkeletonRows } from '@/ui/skeleton';
import { Table, TBody, THead, TR, TH } from '@/ui/table';
import { SessionRow } from '../components/SessionRow';
import { useMySessions } from '../hooks';

export function SessionsSection() {
  const { data, error, isLoading } = useMySessions();
  const count = data?.sessions.length ?? 0;

  return (
    <Card>
      <CardHead
        title="Active sessions"
        aside={
          <span className="font-mono text-[11px] font-normal text-muted">
            {count} signed in
          </span>
        }
      />
      {isLoading ? (
        <CardBody>
          <SkeletonRows rows={2} />
        </CardBody>
      ) : error ? (
        <CardBody>
          <Callout variant="danger">
            <b>Could not load sessions</b>
            <p>{error instanceof ApiError ? error.detail : String(error)}</p>
          </Callout>
        </CardBody>
      ) : !data || data.sessions.length === 0 ? (
        <CardBody>
          <p className="m-0 text-sm text-dim">No active sessions.</p>
        </CardBody>
      ) : (
        <>
          <p className="m-0 border-b border-line-soft px-[18px] py-3 text-[13px] text-dim">
            Browsers signed in to your account. End any session you don&rsquo;t recognise.
          </p>
          <Table>
            <THead>
              <TR>
                <TH>Started</TH>
                <TH>Last seen</TH>
                <TH>IP</TH>
                <TH>User agent</TH>
                <TH align="right">Actions</TH>
              </TR>
            </THead>
            <TBody>
              {data.sessions.map((s) => (
                <SessionRow key={s.id} session={s} />
              ))}
            </TBody>
          </Table>
        </>
      )}
    </Card>
  );
}
