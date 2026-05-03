import { KeyRound } from 'lucide-react';
import type { Module } from '../types';
import ApiKeysPage from './ApiKeysPage';

export const ApiKeysModule: Module = {
  id: 'api-keys',
  label: 'API Keys',
  icon: KeyRound,
  path: '/api-keys',
  element: ApiKeysPage,
  weight: 30,
};
