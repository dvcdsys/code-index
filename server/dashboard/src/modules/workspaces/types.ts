// Shared wire types for the workspaces module. These mirror the OpenAPI
// schemas but are hand-rolled because the generated `components/schemas`
// types are wrapped in `paths[...].get.responses` indirection that's
// noisy to consume directly. When the spec changes, update both.

export type Workspace = {
  id: string;
  name: string;
  description: string;
  created_at: string;
  updated_at: string;
};

export type WorkspaceListResponse = {
  workspaces: Workspace[];
  total: number;
};

export type WebhookMode = 'manual' | 'auto' | 'disabled';

export type RepoStatus =
  | 'pending'
  | 'cloning'
  | 'indexing'
  | 'indexed'
  | 'failed';

export type WorkspaceRepo = {
  id: string;
  workspace_id: string;
  github_url: string;
  branch: string;
  project_path: string;
  token_id: string | null;
  auto_webhook: boolean;
  webhook_mode: WebhookMode;
  status: RepoStatus;
  last_sha: string | null;
  last_error: string | null;
  last_indexed_at: string | null;
  created_at: string;
  updated_at: string;
};

export type WorkspaceRepoListResponse = {
  repos: WorkspaceRepo[];
  total: number;
};

export type GithubToken = {
  id: string;
  name: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string | null;
};

export type GithubTokenListResponse = {
  tokens: GithubToken[];
  total: number;
};

export type GithubRepo = {
  full_name: string;
  default_branch: string;
  private: boolean;
  html_url: string;
  description?: string;
};

export type GithubRepoListResponse = {
  repos: GithubRepo[];
  total: number;
};

export type GithubAccountType = 'user' | 'org';

export type GithubAccount = {
  login: string;
  type: GithubAccountType;
  avatar_url?: string;
};

export type GithubAccountListResponse = {
  accounts: GithubAccount[];
  total: number;
};

export type WorkspaceRepoCreated = {
  repo: WorkspaceRepo;
  webhook_url: string;
  webhook_secret: string;
  auto_registered?: boolean;
  auto_register_note?: string;
};

// Whether the repo's status counts as "still doing something". Polling
// stops as soon as every repo in the workspace is in a terminal state.
export function isInFlight(status: RepoStatus): boolean {
  return status === 'pending' || status === 'cloning' || status === 'indexing';
}
