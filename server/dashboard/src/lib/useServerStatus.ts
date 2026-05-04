import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';

interface StatusPayload {
  server_version: string;
  embedding_model: string;
  model_loaded: boolean;
}

// useServerStatus polls /api/v1/status every 30 seconds. The footer
// reads server_version + model_loaded; the Projects drift indicator
// reads embedding_model. /status is auth-only (not admin-only) so
// viewers also see the footer indicator. model_loaded is set by an
// active Ready(ctx) ping, so it tracks actual sidecar liveness.
//
// queryKey is kept as ['runtime-model'] because server/hooks.ts
// invalidates that key after a sidecar restart to refresh drift
// immediately.
export function useServerStatus() {
  return useQuery({
    queryKey: ['runtime-model'],
    queryFn: ({ signal }) => api.get<StatusPayload>('/status', { signal }),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 30_000,
  });
}

export function useRuntimeModel() {
  const { data } = useServerStatus();
  return data?.embedding_model ?? '';
}
