import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  CreateGroupRequest,
  Group,
  GroupListResponse,
  GroupMemberListResponse,
  UpdateGroupRequest,
} from '@/api/types';

export const groupKeys = {
  all: ['groups'] as const,
  members: (id: string) => ['groups', id, 'members'] as const,
};

export function useGroups() {
  return useQuery({
    queryKey: groupKeys.all,
    queryFn: ({ signal }) => api.get<GroupListResponse>('/groups', { signal }),
  });
}

export function useCreateGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateGroupRequest) => api.post<Group>('/groups', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: groupKeys.all }),
  });
}

export function useUpdateGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateGroupRequest }) =>
      api.patch<Group>(`/groups/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: groupKeys.all }),
  });
}

export function useDeleteGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete<void>(`/groups/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: groupKeys.all }),
  });
}

export function useGroupMembers(id: string, enabled = true) {
  return useQuery({
    queryKey: groupKeys.members(id),
    queryFn: ({ signal }) => api.get<GroupMemberListResponse>(`/groups/${id}/members`, { signal }),
    enabled,
  });
}

export function useAddGroupMember() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) =>
      api.post<void>(`/groups/${id}/members`, { user_id: userId }),
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: groupKeys.members(id) });
      void qc.invalidateQueries({ queryKey: groupKeys.all });
    },
  });
}

export function useRemoveGroupMember() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) =>
      api.delete<void>(`/groups/${id}/members/${userId}`),
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: groupKeys.members(id) });
      void qc.invalidateQueries({ queryKey: groupKeys.all });
    },
  });
}
