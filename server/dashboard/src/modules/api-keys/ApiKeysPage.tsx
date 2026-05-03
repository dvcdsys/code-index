import { useMemo, useState } from 'react';
import { AlertCircle, KeyRound } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Skeleton } from '@/ui/skeleton';
import { Tabs, TabsList, TabsTrigger } from '@/ui/tabs';
import { useAuth } from '@/auth/useAuth';
import { ApiKeyTable } from './components/ApiKeyTable';
import { CreateApiKeyDialog } from './components/CreateApiKeyDialog';
import { useAllApiKeys, useMyApiKeys } from './hooks';

type Mode = 'mine' | 'all';

export default function ApiKeysPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [mode, setMode] = useState<Mode>('mine');

  const mine = useMyApiKeys();
  // Only fetch the All bucket when admin actively switches to it — avoids
  // a wasted request and a redundant refetch on viewers (server would 403).
  const all = useAllApiKeys(isAdmin && mode === 'all');

  const active = mode === 'all' && isAdmin ? all : mine;
  const keys = active.data?.api_keys ?? [];

  const ownerEmailLookup = useMemo(() => {
    // The Owner column would ideally show emails, but the api-keys endpoint
    // returns owner_user_id only. Resolving emails would need /admin/users —
    // skipping that JOIN here keeps this page lean. Render a short id slice
    // until a follow-up adds the lookup. Self-key is highlighted via
    // canRevoke ownership so the audit trail still works.
    return (id: string) =>
      id === user?.id ? user.email : undefined;
  }, [user?.id, user?.email]);

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">API keys</h1>
          <p className="text-sm text-muted-foreground">
            Bearer tokens for CLI / SDK access. Created here, revoked here.
          </p>
        </div>
        <CreateApiKeyDialog />
      </header>

      {isAdmin ? (
        <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
          <TabsList>
            <TabsTrigger value="mine">My keys</TabsTrigger>
            <TabsTrigger value="all">All keys (admin)</TabsTrigger>
          </TabsList>
        </Tabs>
      ) : null}

      {active.isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : active.error ? (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Failed to load API keys</AlertTitle>
          <AlertDescription>
            {active.error instanceof ApiError ? active.error.detail : String(active.error)}
          </AlertDescription>
        </Alert>
      ) : keys.length === 0 ? (
        <EmptyState mode={mode} isAdmin={isAdmin} />
      ) : (
        <ApiKeyTable
          keys={keys}
          showOwner={mode === 'all' && isAdmin}
          ownerEmail={ownerEmailLookup}
          canRevoke={(k) => isAdmin || k.owner_user_id === user?.id}
        />
      )}
    </div>
  );
}

function EmptyState({ mode, isAdmin }: { mode: Mode; isAdmin: boolean }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <KeyRound className="h-10 w-10 text-muted-foreground" />
      <div className="space-y-1">
        <p className="text-base font-medium">
          {mode === 'all' && isAdmin ? 'No API keys exist yet' : 'You have no API keys yet'}
        </p>
        <p className="max-w-sm text-sm text-muted-foreground">
          Create one to authenticate the <code className="rounded bg-muted px-1 py-0.5 text-xs">cix</code>{' '}
          CLI from a workstation.
        </p>
      </div>
    </div>
  );
}
