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

// Project lifecycle status — single source of truth lives on the
// projects row. Surfaces directly on Project (from the projects module)
// and on the WorkspaceProject decorator below.
export type ProjectStatus =
  | 'created'
  | 'pending'
  | 'cloning'
  | 'indexing'
  | 'indexed'
  | 'error'
  | 'failed';

// GitRepo carries the clone + webhook metadata for an external project.
// Local projects have no GitRepo row at all — that's how the dashboard
// tells them apart from cloneable repos.
export type GitRepo = {
  project_path: string;
  path_hash: string;
  github_url: string;
  branch: string;
  token_id: string | null;
  auto_webhook: boolean;
  webhook_mode: WebhookMode;
  last_sha: string | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
};

// WorkspaceProject is what /workspaces/{id}/projects returns: the
// embedded Project plus the membership timestamp.
export type WorkspaceProject = {
  project: {
    host_path: string;
    container_path: string;
    languages: string[];
    status: ProjectStatus;
    path_hash?: string;
    last_indexed_at?: string | null;
    created_at?: string;
    updated_at?: string;
  };
  added_at: string;
};

export type WorkspaceProjectListResponse = {
  projects: WorkspaceProject[];
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

// GitRepoCreated is the POST /git-repos response shape.
export type GitRepoCreated = {
  project: WorkspaceProject['project'];
  git_repo: GitRepo;
  webhook_url: string;
  webhook_secret: string;
  auto_registered?: boolean;
  auto_register_note?: string;
};

// Whether a project's status counts as "still doing something". The
// workspace detail page polls until every linked project is in a
// terminal state.
export function isInFlight(status: ProjectStatus): boolean {
  return (
    status === 'pending' ||
    status === 'cloning' ||
    status === 'indexing'
  );
}
