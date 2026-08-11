import type { Module } from '../types';
import WorkspacesPage from './WorkspacesPage';

export const WorkspacesModule: Module = {
  id: 'workspaces',
  label: 'Workspaces',
  path: '/workspaces',
  element: WorkspacesPage,
  group: 'workspace',
  weight: 25,
  blurb:
    'Group repositories so one query searches all of them at once.',
};
