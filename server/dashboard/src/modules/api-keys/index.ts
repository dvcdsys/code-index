import type { Module } from '../types';
import ApiKeysPage from './ApiKeysPage';

export const ApiKeysModule: Module = {
  id: 'api-keys',
  label: 'API Keys',
  path: '/api-keys',
  element: ApiKeysPage,
  group: 'workspace',
  weight: 30,
  blurb:
    'Bearer tokens for the CLI and CI. Minted here, revoked here.',
};
