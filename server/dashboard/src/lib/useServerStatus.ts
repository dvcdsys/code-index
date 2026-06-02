import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';

interface StatusPayload {
  server_version: string;
  embedding_model: string;
  model_loaded: boolean;
  // Pluggable-provider fields (server >= migration 12). Present on
  // every fresh-built server; older clients may see them as
  // undefined while a rolling upgrade is in progress.
  embedding_provider?: string;
  embedding_provider_manages_process?: boolean;
  // Version-check fields are present only when the server has the
  // versioncheck service wired (see CIX_VERSION_CHECK_ENABLED).
  update_available?: boolean;
  latest_version?: string | null;
  release_url?: string | null;
  version_check?: {
    enabled: boolean;
    checked_at?: string | null;
    error?: string | null;
  };
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
