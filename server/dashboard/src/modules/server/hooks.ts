import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  ModelList,
  RestartAccepted,
  RuntimeConfig,
  RuntimeConfigUpdate,
  SidecarStatus,
} from '@/api/types';

export const serverKeys = {
  runtimeConfig: ['server', 'runtime-config'] as const,
  sidecarStatus: ['server', 'sidecar-status'] as const,
  models: ['server', 'models'] as const,
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
