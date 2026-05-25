import { Cable } from 'lucide-react';
import type { Module } from '../types';
import ManagedTunnelsPage from './ManagedTunnelsPage';

export const ManagedTunnelsModule: Module = {
  id: 'managed-tunnels',
  label: 'Managed Tunnels',
  icon: Cable,
  path: '/tunnels',
  element: ManagedTunnelsPage,
  // Tunnel management exposes the server publicly and holds a secret token —
  // restrict to admins.
  requiredRole: 'admin',
  weight: 36,
};
