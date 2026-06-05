import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { LoginLockListResponse, ResetLoginLockRequest } from '@/api/types';

export const loginLockKeys = {
  all: ['login-locks'] as const,
};

// Locks self-heal as the limiter windows slide, so poll periodically to keep
// the table honest without the admin having to refresh by hand.
export function useLoginLocks() {
  return useQuery({
    queryKey: loginLockKeys.all,
    queryFn: ({ signal }) =>
      api.get<LoginLockListResponse>('/admin/login-locks', { signal }),
    refetchInterval: 15_000,
  });
}

export function useResetLoginLock() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: ResetLoginLockRequest) =>
      api.post<void>('/admin/login-locks/reset', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: loginLockKeys.all }),
  });
}
