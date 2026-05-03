import { HomeModule } from './home';
import type { Module } from './types';

// Static registry of every dashboard feature. Order in the sidebar is
// determined by `weight` (default 100). PR-C/D add Projects, Search,
// API Keys, Users, and Settings here.
export const MODULES: Module[] = [HomeModule].sort(
  (a, b) => (a.weight ?? 100) - (b.weight ?? 100)
);
