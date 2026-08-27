import type { Role } from '@/api/types';
import { ApiKeysModule } from './api-keys';
import { GithubIntegrationModule } from './github-integration';
import { GroupsModule } from './groups';
import { HomeModule } from './home';
import { LoginLocksModule } from './login-locks';
import { ManagedTunnelsModule } from './managed-tunnels';
import { ProjectsModule } from './projects';
import { SearchModule } from './search';
import { SearchStatsModule } from './search-stats';
import { ServerModule } from './server';
import { SettingsModule } from './settings';
import { UsersModule } from './users';
import { WorkspacesModule } from './workspaces';
import type { Module } from './types';

// Static registry of every dashboard feature. Sidebar, router and the
// home-page module grid all read from here — adding a feature is "create the
// folder, register the module".
//
// Workspaces + GitHub Integration ship in every release; those pages render a
// "not configured" callout on 503 (commonly an encryption-key boot failure)
// and the sidebar entries stay visible regardless.
export const MODULES: Module[] = [
  HomeModule,
  ProjectsModule,
  WorkspacesModule,
  SearchModule,
  SearchStatsModule,
  ApiKeysModule,
  GithubIntegrationModule,
  ManagedTunnelsModule,
  UsersModule,
  LoginLocksModule,
  GroupsModule,
  SettingsModule,
  ServerModule,
].sort((a, b) => (a.weight ?? 100) - (b.weight ?? 100));

// A module without `requiredRole` is visible to everyone; one that requires
// `admin` is hidden — and, in App.tsx, not even mounted — for other roles.
export function visibleModules(role: Role | undefined): Module[] {
  return MODULES.filter((m) => m.requiredRole !== 'admin' || role === 'admin');
}
