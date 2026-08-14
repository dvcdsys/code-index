import type { Module } from '../types';
import ProjectsPage from './ProjectsPage';

export const ProjectsModule: Module = {
  id: 'projects',
  label: 'Projects',
  path: '/projects',
  element: ProjectsPage,
  group: 'workspace',
  weight: 10,
  blurb:
    'Indexed repositories — stats, reindex, sync settings, stale-model drift.',
};
