import type { Module } from '../types';
import UsersPage from './UsersPage';

export const UsersModule: Module = {
  id: 'users',
  label: 'Users',
  path: '/users',
  element: UsersPage,
  requiredRole: 'admin',
  group: 'admin',
  weight: 40,
  blurb:
    'Invite teammates, set roles, reset passwords, audit access.',
};
