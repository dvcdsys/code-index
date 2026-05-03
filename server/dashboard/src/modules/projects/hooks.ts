import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  Project,
  ProjectListResponse,
  ProjectSummary,
} from '@/api/types';

export const projectKeys = {
  all: ['projects'] as const,
  detail: (hash: string) => ['projects', hash] as const,
  summary: (hash: string) => ['projects', hash, 'summary'] as const,
};

export function useProjects() {
  return useQuery({
    queryKey: projectKeys.all,
    queryFn: ({ signal }) => api.get<ProjectListResponse>('/projects', { signal }),
  });
}

export function useProject(hash: string | undefined) {
  return useQuery({
    queryKey: hash ? projectKeys.detail(hash) : ['projects', 'unknown'],
    queryFn: ({ signal }) => api.get<Project>(`/projects/${hash}`, { signal }),
    enabled: Boolean(hash),
  });
}

export function useProjectSummary(hash: string | undefined) {
  return useQuery({
    queryKey: hash ? projectKeys.summary(hash) : ['projects', 'unknown', 'summary'],
    queryFn: ({ signal }) => api.get<ProjectSummary>(`/projects/${hash}/summary`, { signal }),
    enabled: Boolean(hash),
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hash: string) => api.delete<void>(`/projects/${hash}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: projectKeys.all }),
  });
}

// NOTE: a "Reindex" button is intentionally absent. The server's three-phase
// indexing protocol (begin → files → finish) requires a producer with filesystem
// access to upload file contents. That is the CLI's job (`cix reindex` /
// `cix watch`). The browser cannot drive this — it has no local filesystem.
// The detail page surfaces this expectation in copy.
