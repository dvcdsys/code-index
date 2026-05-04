import { ServerCog } from 'lucide-react';
import type { Module } from '../types';
import ServerPage from './ServerPage';

export const ServerModule: Module = {
  id: 'server',
  label: 'Server',
  icon: ServerCog,
  path: '/server',
  element: ServerPage,
  requiredRole: 'admin',
  weight: 60,
};
