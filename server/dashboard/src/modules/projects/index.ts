import { Folder } from 'lucide-react';
import type { Module } from '../types';
import ProjectsPage from './ProjectsPage';

export const ProjectsModule: Module = {
  id: 'projects',
  label: 'Projects',
  icon: Folder,
  path: '/projects',
  element: ProjectsPage,
  weight: 10,
};
