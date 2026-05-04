// Hand-written re-exports of the OpenAPI schemas the dashboard actually uses.
//
// The full generated `./generated.ts` is produced by `npm run gen:api` and is
// gitignored — this file gives us stable, named imports without leaking
// `components['schemas']['User']` syntax into every component. Add a new
// alias here when the dashboard starts consuming a new schema.

import type { components } from './generated';

export type Role = 'admin' | 'viewer';

export type User = components['schemas']['User'];
export type UserWithStats = components['schemas']['UserWithStats'];
export type Session = components['schemas']['Session'];
export type ApiKey = components['schemas']['ApiKey'];
export type ApiKeyCreated = components['schemas']['ApiKeyCreated'];
export type ApiKeyListResponse = components['schemas']['ApiKeyListResponse'];

export type Project = components['schemas']['Project'];
export type ProjectSummary = components['schemas']['ProjectSummary'];
export type ProjectStats = components['schemas']['ProjectStats'];
export type ProjectSettings = components['schemas']['ProjectSettings'];
export type ProjectListResponse = components['schemas']['ProjectListResponse'];
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
