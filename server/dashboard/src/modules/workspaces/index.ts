import { Boxes } from 'lucide-react';
import type { Module } from '../types';
import WorkspacesPage from './WorkspacesPage';

export const WorkspacesModule: Module = {
  id: 'workspaces',
  label: 'Workspaces',
  icon: Boxes,
  path: '/workspaces',
  element: WorkspacesPage,
  weight: 25,
};
