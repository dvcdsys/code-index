// Hand-written re-exports of the OpenAPI schemas the dashboard actually uses.
//
// The full generated `./generated.ts` is produced by `npm run gen:api` and is
// gitignored — this file gives us stable, named imports without leaking
// `components['schemas']['User']` syntax into every component. Add a new
// alias here when the dashboard starts consuming a new schema.

import type { components } from './generated';

export type Role = 'admin' | 'viewer';

export type User = components['schemas']['User'];
export type Session = components['schemas']['Session'];
export type ApiKey = components['schemas']['ApiKey'];
export type ApiKeyCreated = components['schemas']['ApiKeyCreated'];

export type Project = components['schemas']['Project'];
export type ProjectSummary = components['schemas']['ProjectSummary'];

export type LoginRequest = components['schemas']['LoginRequest'];
export type LoginResponse = components['schemas']['LoginResponse'];
export type MeResponse = components['schemas']['MeResponse'];
export type ChangePasswordRequest = components['schemas']['ChangePasswordRequest'];
export type CreateUserRequest = components['schemas']['CreateUserRequest'];
export type UpdateUserRequest = components['schemas']['UpdateUserRequest'];
export type CreateApiKeyRequest = components['schemas']['CreateApiKeyRequest'];
export type BootstrapStatusResponse = components['schemas']['BootstrapStatusResponse'];
