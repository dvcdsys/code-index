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
  workspaces: (hash: string) => ['projects', hash, 'workspaces'] as const,
};

// ProjectWorkspaceEntry mirrors the Go response shape from
// /api/v1/projects/{hash}/workspaces — one row per workspace_projects
// membership pointing at this project. The server returns just three
// fields: the workspace it's linked into and the timestamp it was
// added. Branch / status / owner-vs-linked concepts no longer exist
// on this endpoint — projects are uniformly first-class members.
// Defined locally so the hook doesn't depend on a regen of
// generated.ts every time the page renders.
export type ProjectWorkspaceEntry = {
  workspace_id: string;
  workspace_name: string;
  added_at: string;
};

export type ProjectWorkspaceList = {
  workspaces: ProjectWorkspaceEntry[];
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
    // Poll while the project is mid-index so the page reflects completion
    // without a manual refresh. Stops as soon as status flips to indexed/error.
    refetchInterval: (q) => (q.state.data?.status === 'indexing' ? 3000 : false),
  });
}

export function useProjectSummary(hash: string | undefined) {
  return useQuery({
    queryKey: hash ? projectKeys.summary(hash) : ['projects', 'unknown', 'summary'],
    queryFn: ({ signal }) => api.get<ProjectSummary>(`/projects/${hash}/summary`, { signal }),
    enabled: Boolean(hash),
  });
}

// useProjectWorkspaces returns every workspace this project participates
// in. Used by the project detail page to render "Workspaces" chips. The
// endpoint is cheap and the membership is rarely stale, so we don't
// poll — refetch happens on window focus via react-query defaults.
export function useProjectWorkspaces(hash: string | undefined) {
  return useQuery({
    queryKey: hash ? projectKeys.workspaces(hash) : ['projects', 'unknown', 'workspaces'],
    queryFn: ({ signal }) =>
      api.get<ProjectWorkspaceList>(`/projects/${hash}/workspaces`, { signal }),
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

// Reindex is only meaningful for GitHub-cloned projects — the server enqueues a
// clone_repo job that chains into index_repo. Local projects must reindex via
// the CLI (`cix reindex` / `cix watch`); the endpoint returns 422 for those.
//
// `full: true` clears indexed_sha server-side so the job does a full reindex
// instead of the default tree.Diff-driven incremental. Use as an escape hatch
// when index drift is suspected (model change is auto-detected and forces full
// without needing this flag).
export type ReindexArgs = { hash: string; full?: boolean };
export type ReindexResponse = {
  status: 'enqueued' | 'already_running';
  mode?: 'full' | 'incremental';
};

export function useReindexProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ hash, full }: ReindexArgs) => {
      const qs = full ? '?full=true' : '';
      return api.post<ReindexResponse>(`/projects/${hash}/reindex${qs}`, undefined);
    },
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: projectKeys.detail(vars.hash) });
      qc.invalidateQueries({ queryKey: projectKeys.all });
    },
  });
}
