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
  gitRepo: (hash: string) => ['projects', hash, 'git-repo'] as const,
};

// GitRepo mirrors the server's git_repos payload for an external project.
// Defined locally so the project page doesn't depend on a regen of
// generated.ts. Only the fields the sync UI reads are typed.
export type GitRepo = {
  webhook_mode: 'manual' | 'auto' | 'disabled';
  polling_enabled: boolean;
  poll_interval_seconds: number | null;
  next_poll_at: string | null;
  last_sha: string | null;
  last_error: string | null;
};

// The three sync methods the project page exposes. Maps on the server to
// (webhook_mode, polling_enabled): webhook→auto, polling→disabled+polling,
// manual→disabled+no-polling.
export type SyncMethod = 'webhook' | 'polling' | 'manual';

export type UpdateSyncArgs = {
  hash: string;
  sync_method: SyncMethod;
  poll_interval_seconds?: number;
};

export type UpdateSyncResult = {
  git_repo: GitRepo;
  note?: string;
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

// useProjectGitRepo fetches the git_repos metadata for an external project.
// 404 for local projects — the caller gates on host_path starting with
// "github.com/". Polls while a poll is pending so next_poll_at stays fresh.
export function useProjectGitRepo(hash: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: hash ? projectKeys.gitRepo(hash) : ['projects', 'unknown', 'git-repo'],
    queryFn: ({ signal }) => api.get<GitRepo>(`/projects/${hash}/git-repo`, { signal }),
    enabled: Boolean(hash) && enabled,
  });
}

// WebhookInfo is the per-project webhook delivery URL + HMAC secret an
// operator pastes into GitHub when setting the hook up by hand.
export type WebhookInfo = {
  webhook_url: string;
  webhook_secret: string;
  auto_registered: boolean;
};

// useProjectWebhookInfo fetches the webhook URL + secret for manual setup.
// Admin-only data (the secret is sensitive) — gate the call on isAdmin.
export function useProjectWebhookInfo(hash: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: hash ? ['projects', hash, 'webhook-info'] : ['projects', 'unknown', 'webhook-info'],
    queryFn: ({ signal }) => api.get<WebhookInfo>(`/projects/${hash}/webhook-info`, { signal }),
    enabled: Boolean(hash) && enabled,
  });
}

// useUpdateProjectSync switches a project's sync method (webhook / polling /
// manual) from the project page. Invalidates the git-repo + detail queries so
// the card and header reflect the new configuration.
export function useUpdateProjectSync() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ hash, sync_method, poll_interval_seconds }: UpdateSyncArgs) =>
      api.patch<UpdateSyncResult>(`/projects/${hash}/git-repo`, {
        sync_method,
        ...(poll_interval_seconds ? { poll_interval_seconds } : {}),
      }),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: projectKeys.gitRepo(vars.hash) });
      qc.invalidateQueries({ queryKey: projectKeys.detail(vars.hash) });
      qc.invalidateQueries({ queryKey: ['projects', vars.hash, 'webhook-info'] });
    },
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

// Force-stop is external-only too: it hard-aborts the clone+index pipeline
// (clears queued clone/index jobs + cancels the live index session). 422 for
// local projects, which have no server-side pipeline. cancelled=true means a
// live session was caught; jobs_cleared counts the queued jobs removed.
export type ForceStopResponse = {
  cancelled: boolean;
  jobs_cleared: number;
};

export function useForceStopIndex() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hash: string) =>
      api.post<ForceStopResponse>(`/projects/${hash}/force-stop`, undefined),
    onSuccess: (_data, hash) => {
      qc.invalidateQueries({ queryKey: projectKeys.detail(hash) });
      qc.invalidateQueries({ queryKey: projectKeys.all });
    },
  });
}
