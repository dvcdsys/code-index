import type { Module } from '../types';
import GithubIntegrationPage from './GithubIntegrationPage';

export const GithubIntegrationModule: Module = {
  id: 'github-integration',
  label: 'GitHub Integration',
  path: '/github-integration',
  element: GithubIntegrationPage,
  // Every endpoint under this page (github-tokens CRUD, webhook origin,
  // reconcile) is admin-only on the backend. Hide the sidebar entry, the
  // home-page card, and the route mount itself from non-admin users.
  requiredRole: 'admin',
  group: 'workspace',
  weight: 35,
  blurb:
    'Personal access tokens and webhook wiring for external repositories.',
};
