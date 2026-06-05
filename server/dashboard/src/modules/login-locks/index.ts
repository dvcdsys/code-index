import { ShieldAlert } from 'lucide-react';
import type { Module } from '../types';
import LoginLocksPage from './LoginLocksPage';

export const LoginLocksModule: Module = {
  id: 'login-locks',
  label: 'Login security',
  icon: ShieldAlert,
  path: '/login-locks',
  element: LoginLocksPage,
  requiredRole: 'admin',
  weight: 45,
};
