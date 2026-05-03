import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  ApiKeyCreated,
  ApiKeyListResponse,
  CreateApiKeyRequest,
} from '@/api/types';

// Owned-keys vs admin-only "all keys" view share an endpoint differentiated
// by the `?owner=all` query param. Two cache buckets so an admin toggling
// between the views doesn't see a stale slice from the other.
export const apiKeyKeys = {
  mine: ['apikeys', 'mine'] as const,
  all: ['apikeys', 'all'] as const,
};

export function useMyApiKeys() {
  return useQuery({
    queryKey: apiKeyKeys.mine,
    queryFn: ({ signal }) => api.get<ApiKeyListResponse>('/api-keys', { signal }),
  });
}

export function useAllApiKeys(enabled: boolean) {
  return useQuery({
    queryKey: apiKeyKeys.all,
    queryFn: ({ signal }) =>
      api.get<ApiKeyListResponse>('/api-keys', { signal, query: { owner: 'all' } }),
    enabled,
  });
}

export function useCreateApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateApiKeyRequest) => api.post<ApiKeyCreated>('/api-keys', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: apiKeyKeys.mine });
      qc.invalidateQueries({ queryKey: apiKeyKeys.all });
    },
  });
}

export function useRevokeApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete<void>(`/api-keys/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: apiKeyKeys.mine });
      qc.invalidateQueries({ queryKey: apiKeyKeys.all });
    },
  });
}
