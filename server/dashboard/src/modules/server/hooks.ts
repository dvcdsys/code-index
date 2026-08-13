import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import { isActivePhase } from '@/api/types';
import type {
  ActiveEmbeddingProvider,
  AutoVacuumRequest,
  DatabaseState,
  MaintenanceOperation,
  ScheduledTask,
  ScheduleUpdate,
  ReclaimRequest,
  ReclaimResult,
  CleanRequest,
  CleanResult,
  EmbeddingProviderList,
  ModelList,
  ReclaimAnalysis,
  ResourceUsage,
  RestartAccepted,
  RuntimeConfig,
  RuntimeConfigUpdate,
  SidecarStatus,
  SwitchEmbeddingProviderRequest,
  TestEmbeddingProviderResponse,
} from '@/api/types';

export const serverKeys = {
  runtimeConfig: ['server', 'runtime-config'] as const,
  sidecarStatus: ['server', 'sidecar-status'] as const,
  models: ['server', 'models'] as const,
  embeddingProviders: ['server', 'embedding-providers'] as const,
  activeProvider: ['server', 'embedding-provider', 'active'] as const,
  resources: ['server', 'resources'] as const,
  database: ['server', 'database'] as const,
  schedules: ['server', 'schedules'] as const,
};

export function useRuntimeConfig() {
  return useQuery({
    queryKey: serverKeys.runtimeConfig,
    queryFn: ({ signal }) => api.get<RuntimeConfig>('/admin/runtime-config', { signal }),
  });
}

export function useUpdateRuntimeConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: RuntimeConfigUpdate) =>
      api.put<RuntimeConfig>('/admin/runtime-config', patch),
    onSuccess: (data) => {
      // Replace the cached value so the form switches to "DB"-sourced
      // pills before the dashboard issues the restart call.
      qc.setQueryData(serverKeys.runtimeConfig, data);
    },
  });
}

export function useSidecarStatus() {
  return useQuery({
    queryKey: serverKeys.sidecarStatus,
    queryFn: ({ signal }) => api.get<SidecarStatus>('/admin/sidecar/status', { signal }),
    // Poll every second whenever a restart is in flight; otherwise back off
    // to 5s — the status almost never changes outside of admin actions and
    // we don't want to thrash on idle dashboards.
    refetchInterval: (q) => {
      const data = q.state.data as SidecarStatus | undefined;
      if (!data) return 2_000;
      if (data.restart_in_flight || data.state === 'starting' || data.state === 'restarting') {
        return 1_000;
      }
      return 5_000;
    },
    refetchIntervalInBackground: false,
  });
}

export function useRestartSidecar() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<RestartAccepted>('/admin/sidecar/restart'),
    onSettled: () => {
      // Force a status refetch immediately so the UI flips to "restarting"
      // without waiting for the next poll tick. Also invalidate the cached
      // runtime model — drift indicators on Projects depend on it being
      // current after a model swap.
      qc.invalidateQueries({ queryKey: serverKeys.sidecarStatus });
      qc.invalidateQueries({ queryKey: ['runtime-model'] });
    },
  });
}

export function useGGUFModels() {
  return useQuery({
    queryKey: serverKeys.models,
    queryFn: ({ signal }) => api.get<ModelList>('/admin/models', { signal }),
    // Cache aggressively: GGUFs only change when the operator runs
    // `cix init` or manually drops a file in the cache.
    staleTime: 60_000,
  });
}

// useEmbeddingProviders returns the list of registered provider
// kinds, their schemas, and which API-key env vars are currently set
// on the server. Polled occasionally so a freshly-exported env var
// flips the missing-key banner without a hard reload.
export function useEmbeddingProviders() {
  return useQuery({
    queryKey: serverKeys.embeddingProviders,
    queryFn: ({ signal }) =>
      api.get<EmbeddingProviderList>('/admin/embedding-providers', { signal }),
    staleTime: 30_000,
  });
}

// useActiveProvider returns the persisted active provider + config.
// Invalidated by useSwitchProvider on success.
export function useActiveProvider() {
  return useQuery({
    queryKey: serverKeys.activeProvider,
    queryFn: ({ signal }) =>
      api.get<ActiveEmbeddingProvider>('/admin/embedding-providers/active', { signal }),
  });
}

// useResourceUsage reports memory and disk. The disk figures come from real
// directory walks — several seconds on a large index — so this is deliberately
// NOT polled: it refetches when the section mounts and after a clean.
export function useResourceUsage() {
  return useQuery({
    queryKey: serverKeys.resources,
    queryFn: ({ signal }) => api.get<ResourceUsage>('/admin/resources', { signal }),
    staleTime: 30_000,
  });
}

// useAnalyzeReclaimable is a mutation, not a query, on purpose: it is
// admin-triggered, expensive, and has a server-side side effect (it caches the
// analysis the clean call redeems). As a query it would fire on mount and on
// every cache invalidation. Its `data` is the rendered analysis, and
// `reset()` is how the section clears it.
export function useAnalyzeReclaimable() {
  return useMutation({
    mutationFn: () => api.post<ReclaimAnalysis>('/admin/resources/analyze'),
  });
}

export function useCleanResources() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CleanRequest) => api.post<CleanResult>('/admin/resources/clean', body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: serverKeys.resources });
      // A clean can drop vector collections and cloned checkouts, so the
      // per-project storage numbers on Projects are stale afterwards.
      qc.invalidateQueries({ queryKey: ['projects'] });
    },
  });
}

// useTestProvider calls /test for a given kind+config. Doesn't
// touch the active state on the server.
export function useTestProvider(kind: string) {
  return useMutation({
    mutationFn: (config: Record<string, unknown>) =>
      api.post<TestEmbeddingProviderResponse>(
        `/admin/embedding-providers/${encodeURIComponent(kind)}/test`,
        config
      ),
  });
}

// useSwitchProvider PUTs the new selection. On success: invalidate
// the active-provider cache, the /status cache (footer indicator),
// and the sidecar-status cache (the latter goes to "n/a" for
// non-ollama providers).
export function useSwitchProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: SwitchEmbeddingProviderRequest) =>
      api.put<ActiveEmbeddingProvider>('/admin/embedding-providers/active', req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: serverKeys.activeProvider });
      qc.invalidateQueries({ queryKey: serverKeys.sidecarStatus });
      qc.invalidateQueries({ queryKey: ['runtime-model'] });
    },
  });
}

// ---------------------------------------------------------------------------
// Database compaction
// ---------------------------------------------------------------------------

export function useDatabaseState() {
  return useQuery({
    queryKey: serverKeys.database,
    queryFn: ({ signal }) => api.get<DatabaseState>('/admin/database', { signal }),
    // Poll quickly while an operation is in flight so the numbers and the
    // button state track it; otherwise leave it alone — page geometry only
    // changes when something is done to it.
    //
    // Except on failure, where leaving it alone is exactly wrong. This is the
    // one card that can take its own backend away: a compaction restarts the
    // server, every request from here fails for about a minute, and with no
    // interval and no cached data there is nothing left to trigger a refetch.
    // Observed live — the sibling queries came back on their own intervals
    // while this card sat on "Failed to fetch" until the page was reloaded.
    refetchInterval: (q) => {
      if (q.state.status === 'error') return 3_000;
      const data = q.state.data as DatabaseState | undefined;
      return isActivePhase(data?.operation?.phase) ? 2_000 : false;
    },
    refetchIntervalInBackground: false,
  });
}

// Every recurring task the server knows how to run, not just the database's.
// The registry is generic, so this hook is the one place the dashboard will
// read from as more tasks are hung off it.
export function useSchedules() {
  return useQuery({
    queryKey: serverKeys.schedules,
    queryFn: ({ signal }) =>
      api.get<{ tasks: ScheduledTask[] }>('/admin/schedules', { signal }).then((r) => r.tasks),
  });
}

export function useUpdateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, ...patch }: ScheduleUpdate & { name: string }) =>
      api.put<ScheduledTask>(`/admin/schedules/${encodeURIComponent(name)}`, patch),
    // The server re-arms on save and returns the task as it now resolves —
    // including the next runs it will actually keep — so the response replaces
    // the cached entry rather than triggering a refetch that could race it.
    onSuccess: (task) =>
      qc.setQueryData(serverKeys.schedules, (prev: ScheduledTask[] | undefined) =>
        prev ? prev.map((t) => (t.name === task.name ? task : t)) : [task]),
  });
}

export function useReclaimFreePages() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: ReclaimRequest) => api.post<ReclaimResult>('/admin/database/reclaim', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: serverKeys.database }),
  });
}

// Compaction is a mutation rather than a query for the same reason analyze is:
// it has a side effect that must never fire on a mount or a refetch. It
// answers 202 and the server then restarts itself, so the banner — not this
// hook — is what reports the outcome.
export function useCompactDatabase() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<MaintenanceOperation>('/admin/database/compact'),
    onSuccess: () => qc.invalidateQueries({ queryKey: serverKeys.database }),
  });
}

// The reclaim mode is a setting, not an operation. Changing it happens to cost
// a rebuild, which is why this looks like compaction — but asking for the mode
// the database is already in does nothing at all.
export function useSetAutoVacuum() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AutoVacuumRequest) =>
      api.put<MaintenanceOperation>('/admin/database/auto-vacuum', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: serverKeys.database }),
  });
}
