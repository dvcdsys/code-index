import { useMemo, useState } from 'react';
import { ApiError } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { Callout } from '@/ui/alert';
import { Empty } from '@/ui/card';
import { Chip } from '@/ui/code';
import { Input } from '@/ui/input';
import { Page } from '@/ui/page';
import { SkeletonRows } from '@/ui/skeleton';
import { Segmented } from '@/ui/tabs';
import { TableNote } from '@/ui/table';
import { useStatusFact } from '@/app/StatusBar';
import { ApiKeyTable } from './components/ApiKeyTable';
import { CreateApiKeyDialog } from './components/CreateApiKeyDialog';
import { useAllApiKeys, useMyApiKeys } from './hooks';

type Mode = 'mine' | 'all';

// One table, one scope switch, one primary action. The "shown only once"
// caveat lives under the table as a mono line rather than only inside the
// creation dialog — by the time you meet it there it is already too late.
export default function ApiKeysPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [mode, setMode] = useState<Mode>('mine');
  const [filter, setFilter] = useState('');

  const mine = useMyApiKeys();
  // Only fetch the All bucket once an admin switches to it — a viewer would
  // get a 403 and the request would be wasted.
  const all = useAllApiKeys(isAdmin && mode === 'all');

  const active = mode === 'all' && isAdmin ? all : mine;
  const keys = active.data?.api_keys ?? [];

  const q = filter.trim().toLowerCase();
  const shown = q ? keys.filter((k) => k.name.toLowerCase().includes(q)) : keys;

  useStatusFact(
    active.isLoading ? null : `${keys.length} key${keys.length === 1 ? '' : 's'}`
  );

  // The endpoint returns owner_user_id only; resolving every id to an email
  // would mean a /admin/users join on a page that doesn't otherwise need one.
  // Own keys show the real address, the rest show a short id.
  const ownerEmail = useMemo(
    () => (id: string) => (id === user?.id ? user.email : undefined),
    [user?.id, user?.email]
  );

  return (
    <Page
      title="API keys"
      subtitle="Bearer tokens for the CLI and CI. Created here, revoked here."
      action={<CreateApiKeyDialog />}
    >
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        {isAdmin ? (
          <Segmented
            aria-label="Key scope"
            value={mode}
            onChange={setMode}
            options={[
              { value: 'mine', label: 'My keys' },
              { value: 'all', label: 'All keys (admin)' },
            ]}
          />
        ) : (
          <span />
        )}
        <Input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter by name…"
          aria-label="Filter keys by name"
          className="w-full sm:w-64"
        />
      </div>

      {active.isLoading ? (
        <SkeletonRows rows={4} />
      ) : active.error ? (
        <Callout variant="danger">
          <b>Could not load API keys</b>
          <p>
            {active.error instanceof ApiError ? active.error.detail : String(active.error)}
          </p>
        </Callout>
      ) : keys.length === 0 ? (
        <Empty title={mode === 'all' && isAdmin ? 'No API keys exist yet' : 'No API keys yet'}>
          Create one to authenticate the <Chip>cix</Chip> CLI from a workstation or a CI job.
        </Empty>
      ) : (
        <>
          <ApiKeyTable
            keys={shown}
            showOwner={mode === 'all' && isAdmin}
            ownerEmail={ownerEmail}
            canRevoke={(k) => isAdmin || k.owner_user_id === user?.id}
          />
          <TableNote
            left={`${shown.length}${shown.length === keys.length ? '' : ` of ${keys.length}`} key${keys.length === 1 ? '' : 's'}`}
            right="the full key is shown once — immediately after creation"
          />
        </>
      )}
    </Page>
  );
}
