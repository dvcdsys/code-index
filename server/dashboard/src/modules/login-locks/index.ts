import type { Module } from '../types';
import LoginLocksPage from './LoginLocksPage';

export const LoginLocksModule: Module = {
  id: 'login-locks',
  label: 'Login security',
  path: '/login-locks',
  element: LoginLocksPage,
  requiredRole: 'admin',
  group: 'admin',
  weight: 45,
  blurb:
    'Accounts locked out by failed sign-ins, and how to release them.',
};
