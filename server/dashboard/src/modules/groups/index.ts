import type { Module } from '../types';
import GroupsPage from './GroupsPage';

export const GroupsModule: Module = {
  id: 'groups',
  label: 'View Groups',
  path: '/groups',
  element: GroupsPage,
  requiredRole: 'admin',
  group: 'admin',
  weight: 46,
  blurb:
    'View groups that external projects and workspaces can be shared to.',
};
