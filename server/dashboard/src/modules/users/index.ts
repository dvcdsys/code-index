import { Users } from 'lucide-react';
import type { Module } from '../types';
import UsersPage from './UsersPage';

export const UsersModule: Module = {
  id: 'users',
  label: 'Users',
  icon: Users,
  path: '/users',
  element: UsersPage,
  requiredRole: 'admin',
  weight: 40,
};
