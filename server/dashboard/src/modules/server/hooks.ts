import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  ActiveEmbeddingProvider,
  EmbeddingProviderList,
  ModelList,
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
