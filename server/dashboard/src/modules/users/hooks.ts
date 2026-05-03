import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  CreateUserRequest,
  UpdateUserRequest,
  User,
  UserListResponse,
} from '@/api/types';

export const userKeys = {
  all: ['users'] as const,
};

export function useUsers() {
  return useQuery({
    queryKey: userKeys.all,
    queryFn: ({ signal }) => api.get<UserListResponse>('/admin/users', { signal }),
  });
}

export function useCreateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateUserRequest) => api.post<User>('/admin/users', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: userKeys.all }),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateUserRequest }) =>
      api.patch<User>(`/admin/users/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: userKeys.all }),
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete<void>(`/admin/users/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: userKeys.all }),
  });
}
