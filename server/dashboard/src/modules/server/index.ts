import type { Module } from '../types';
import ServerPage from './ServerPage';

export const ServerModule: Module = {
  id: 'server',
  label: 'Server',
  path: '/server',
  element: ServerPage,
  requiredRole: 'admin',
  group: 'admin',
  weight: 60,
  blurb:
    'Embedding provider, model, indexing parameters and sidecar lifecycle.',
};
