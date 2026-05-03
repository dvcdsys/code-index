import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { ChangePasswordRequest, SessionListResponse } from '@/api/types';

export const settingsKeys = {
  sessions: ['auth', 'sessions'] as const,
};

export function useMySessions() {
  return useQuery({
    queryKey: settingsKeys.sessions,
    queryFn: ({ signal }) => api.get<SessionListResponse>('/auth/sessions', { signal }),
  });
}

export function useDeleteSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete<void>(`/auth/sessions/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKeys.sessions }),
  });
}

// Change-password — no useQueryClient invalidation; the caller is expected
// to logout immediately afterwards (server already revoked sibling sessions
// for us, only the current one survives, and we want a fresh login anyway).
export function useChangePassword() {
  return useMutation({
    mutationFn: (body: ChangePasswordRequest) =>
      api.post<void>('/auth/change-password', body),
  });
}
