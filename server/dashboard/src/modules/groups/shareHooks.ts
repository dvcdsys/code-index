import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { GroupIdListResponse } from '@/api/types';

// Project ↔ group shares (admin only; external projects).
export function useProjectShares(hash: string, enabled = true) {
  return useQuery({
    queryKey: ['project-shares', hash],
    queryFn: ({ signal }) => api.get<GroupIdListResponse>(`/projects/${hash}/shares`, { signal }),
    enabled,
  });
}

export function useShareProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ hash, groupId }: { hash: string; groupId: string }) =>
      api.post<void>(`/projects/${hash}/shares`, { group_id: groupId }),
    onSuccess: (_d, { hash }) => qc.invalidateQueries({ queryKey: ['project-shares', hash] }),
  });
}

export function useUnshareProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ hash, groupId }: { hash: string; groupId: string }) =>
      api.delete<void>(`/projects/${hash}/shares/${groupId}`),
    onSuccess: (_d, { hash }) => qc.invalidateQueries({ queryKey: ['project-shares', hash] }),
  });
}

// Workspace ↔ group shares (owner-in-group or admin).
export function useWorkspaceShares(id: string, enabled = true) {
  return useQuery({
    queryKey: ['workspace-shares', id],
    queryFn: ({ signal }) => api.get<GroupIdListResponse>(`/workspaces/${id}/shares`, { signal }),
    enabled,
  });
}

export function useShareWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, groupId }: { id: string; groupId: string }) =>
      api.post<void>(`/workspaces/${id}/shares`, { group_id: groupId }),
    onSuccess: (_d, { id }) => qc.invalidateQueries({ queryKey: ['workspace-shares', id] }),
  });
}

export function useUnshareWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, groupId }: { id: string; groupId: string }) =>
      api.delete<void>(`/workspaces/${id}/shares/${groupId}`),
    onSuccess: (_d, { id }) => qc.invalidateQueries({ queryKey: ['workspace-shares', id] }),
  });
}

// Reassign a local project's owner (admin only).
export function useReassignProjectOwner() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ hash, ownerUserId }: { hash: string; ownerUserId: string }) =>
      api.put<void>(`/projects/${hash}/owner`, { owner_user_id: ownerUserId }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
  });
}
