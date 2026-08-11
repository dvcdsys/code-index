import type { Module } from '../types';
import ManagedTunnelsPage from './ManagedTunnelsPage';

export const ManagedTunnelsModule: Module = {
  id: 'managed-tunnels',
  label: 'Managed Tunnels',
  path: '/tunnels',
  element: ManagedTunnelsPage,
  // Tunnel management exposes the server publicly and holds a secret token —
  // restrict to admins.
  requiredRole: 'admin',
  group: 'workspace',
  weight: 36,
  blurb:
    'Outbound tunnel that gives GitHub a public webhook URL behind NAT.',
};
