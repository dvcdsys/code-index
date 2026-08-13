// Hand-written re-exports of the OpenAPI schemas the dashboard actually uses.
//
// The full generated `./generated.ts` is produced by `npm run gen:api` and is
// gitignored — this file gives us stable, named imports without leaking
// `components['schemas']['User']` syntax into every component. Add a new
// alias here when the dashboard starts consuming a new schema.

import type { components } from './generated';

export type Role = 'admin' | 'user';

export type User = components['schemas']['User'];

export type Group = components['schemas']['Group'];
export type GroupMember = components['schemas']['GroupMember'];
export type GroupListResponse = components['schemas']['GroupListResponse'];
export type GroupMemberListResponse = components['schemas']['GroupMemberListResponse'];
export type CreateGroupRequest = components['schemas']['CreateGroupRequest'];
export type UpdateGroupRequest = components['schemas']['UpdateGroupRequest'];
export type GroupIdListResponse = components['schemas']['GroupIdListResponse'];
export type UserWithStats = components['schemas']['UserWithStats'];
export type ResetUserPasswordRequest = components['schemas']['ResetUserPasswordRequest'];
export type LoginLock = components['schemas']['LoginLock'];
export type LoginLockListResponse = components['schemas']['LoginLockListResponse'];
export type ResetLoginLockRequest = components['schemas']['ResetLoginLockRequest'];
export type Session = components['schemas']['Session'];
export type ApiKey = components['schemas']['ApiKey'];
export type ApiKeyCreated = components['schemas']['ApiKeyCreated'];
export type ApiKeyListResponse = components['schemas']['ApiKeyListResponse'];

export type Project = components['schemas']['Project'];
export type ProjectSummary = components['schemas']['ProjectSummary'];
export type ProjectStats = components['schemas']['ProjectStats'];
export type ProjectSettings = components['schemas']['ProjectSettings'];
export type ProjectListResponse = components['schemas']['ProjectListResponse'];
export type IndexProgressResponse = components['schemas']['IndexProgressResponse'];
export type IndexProgressInfo = components['schemas']['IndexProgressInfo'];
export type DirEntry = components['schemas']['DirEntry'];
export type SymbolEntry = components['schemas']['SymbolEntry'];

export type SemanticSearchRequest = components['schemas']['SemanticSearchRequest'];
export type SemanticSearchResponse = components['schemas']['SemanticSearchResponse'];
export type FileGroupResult = components['schemas']['FileGroupResult'];
export type FileMatch = components['schemas']['FileMatch'];
export type NestedHit = components['schemas']['NestedHit'];

export type SymbolSearchRequest = components['schemas']['SymbolSearchRequest'];
export type SymbolSearchResponse = components['schemas']['SymbolSearchResponse'];
export type SymbolResultItem = components['schemas']['SymbolResultItem'];

export type DefinitionRequest = components['schemas']['DefinitionRequest'];
export type DefinitionResponse = components['schemas']['DefinitionResponse'];
export type DefinitionItem = components['schemas']['DefinitionItem'];

export type ReferenceRequest = components['schemas']['ReferenceRequest'];
export type ReferenceResponse = components['schemas']['ReferenceResponse'];
export type ReferenceItem = components['schemas']['ReferenceItem'];

export type FileSearchRequest = components['schemas']['FileSearchRequest'];
export type FileSearchResponse = components['schemas']['FileSearchResponse'];
export type FileResultItem = components['schemas']['FileResultItem'];

export type LoginRequest = components['schemas']['LoginRequest'];
export type LoginResponse = components['schemas']['LoginResponse'];
export type MeResponse = components['schemas']['MeResponse'];
export type ChangePasswordRequest = components['schemas']['ChangePasswordRequest'];
export type CreateUserRequest = components['schemas']['CreateUserRequest'];
export type UpdateUserRequest = components['schemas']['UpdateUserRequest'];
export type UserListResponse = components['schemas']['UserListResponse'];
export type CreateApiKeyRequest = components['schemas']['CreateApiKeyRequest'];
export type SessionListResponse = components['schemas']['SessionListResponse'];
export type BootstrapStatusResponse = components['schemas']['BootstrapStatusResponse'];

export type RuntimeConfig = components['schemas']['RuntimeConfig'];
export type RuntimeConfigUpdate = components['schemas']['RuntimeConfigUpdate'];
export type RuntimeConfigRecommended = components['schemas']['RuntimeConfigRecommended'];
export type SidecarStatus = components['schemas']['SidecarStatus'];
export type ModelEntry = components['schemas']['ModelEntry'];
export type ModelList = components['schemas']['ModelList'];
export type RestartAccepted = components['schemas']['RestartAccepted'];

export type EmbeddingProviderInfo = components['schemas']['EmbeddingProviderInfo'];
export type EmbeddingProviderSecretEnv = components['schemas']['EmbeddingProviderSecretEnv'];
export type EmbeddingProviderList = components['schemas']['EmbeddingProviderList'];
export type ActiveEmbeddingProvider = components['schemas']['ActiveEmbeddingProvider'];
export type SwitchEmbeddingProviderRequest = components['schemas']['SwitchEmbeddingProviderRequest'];
export type TestEmbeddingProviderResponse = components['schemas']['TestEmbeddingProviderResponse'];

// Provider kind union — the dashboard uses this in form-state discriminants.
export type EmbeddingProviderKind = 'ollama' | 'openai' | 'voyage';

// Admin resource accounting: what the server is using, what of that is
// reclaimable, and the result of reclaiming it.
export type ResourceUsage = components['schemas']['ResourceUsage'];
export type MemoryUsage = components['schemas']['MemoryUsage'];
export type DiskUsage = components['schemas']['DiskUsage'];
export type VectorStoreUsage = components['schemas']['VectorStoreUsage'];
export type ReclaimAnalysis = components['schemas']['ReclaimAnalysis'];
export type ReclaimCategory = components['schemas']['ReclaimCategory'];
export type ReclaimCategoryId = components['schemas']['ReclaimCategoryId'];
export type ReclaimItem = components['schemas']['ReclaimItem'];
export type CleanRequest = components['schemas']['CleanRequest'];
export type CleanResult = components['schemas']['CleanResult'];
export type CleanCategoryResult = components['schemas']['CleanCategoryResult'];

// Database compaction: how much of the SQLite file is wasted, what an
// operation on it is doing, and when one runs automatically.
export type DatabaseState = components['schemas']['DatabaseState'];
export type AutoVacuumRequest = components['schemas']['AutoVacuumRequest'];
export type MaintenanceOperation = components['schemas']['MaintenanceOperation'];
export type MaintenanceEvent = components['schemas']['MaintenanceEvent'];
export type ReclaimRequest = components['schemas']['ReclaimRequest'];
export type ReclaimResult = components['schemas']['ReclaimResult'];
// Recurring tasks. Not database-specific — the registry is generic and the
// database's reclaim and compaction are simply its first two entries.
export type ScheduledTask = components['schemas']['ScheduledTask'];
export type ScheduleUpdate = components['schemas']['ScheduleUpdate'];

// Phases in which something is actually happening to the database, as opposed
// to the terminal ones that only report what happened.
//
// One definition, because three components need the answer — the banner, the
// polling interval and the disabled state of the controls — and three copies
// had already started to disagree about which phases count.
const ACTIVE_PHASES: ReadonlySet<string> = new Set([
  'preparing',
  'copying',
  'ready_to_swap',
  'swapping',
  'restarting',
]);

export function isActivePhase(phase: string | null | undefined): boolean {
  return !!phase && ACTIVE_PHASES.has(phase);
}
