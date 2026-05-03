import { ApiKeysModule } from './api-keys';
import { HomeModule } from './home';
import { ProjectsModule } from './projects';
import { SearchModule } from './search';
import { SettingsModule } from './settings';
import { UsersModule } from './users';
import type { Module } from './types';

// Static registry of every dashboard feature. Order in the sidebar is
// determined by `weight` (default 100). PR-D adds API Keys, Users, Settings.
export const MODULES: Module[] = [
  HomeModule,
  ProjectsModule,
  SearchModule,
  ApiKeysModule,
  UsersModule,
  SettingsModule,
].sort((a, b) => (a.weight ?? 100) - (b.weight ?? 100));
