import { ApiKeysModule } from './api-keys';
import { GithubIntegrationModule } from './github-integration';
import { GroupsModule } from './groups';
import { HomeModule } from './home';
import { LoginLocksModule } from './login-locks';
import { ManagedTunnelsModule } from './managed-tunnels';
import { ProjectsModule } from './projects';
import { SearchModule } from './search';
import { ServerModule } from './server';
import { SettingsModule } from './settings';
import { UsersModule } from './users';
import { WorkspacesModule } from './workspaces';
import type { Module } from './types';

// Static registry of every dashboard feature. Order in the sidebar is
// determined by `weight` (default 100). PR-D adds API Keys, Users, Settings.
// PR-E adds Server (admin-only runtime config + sidecar lifecycle).
// Workspaces + GitHub Integration ship in every release; the pages render a
// "not configured" alert on 503 (commonly an encryption-key boot
// failure) and the sidebar entries stay visible regardless. Managed Tunnels
// orchestrates the optional outbound tunnel that provides the public
// webhook URL.
export const MODULES: Module[] = [
  HomeModule,
  ProjectsModule,
  WorkspacesModule,
  SearchModule,
  ApiKeysModule,
  GithubIntegrationModule,
  ManagedTunnelsModule,
  UsersModule,
  LoginLocksModule,
  GroupsModule,
  SettingsModule,
  ServerModule,
].sort((a, b) => (a.weight ?? 100) - (b.weight ?? 100));
