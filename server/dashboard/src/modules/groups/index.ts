import { UsersRound } from 'lucide-react';
import type { Module } from '../types';
import GroupsPage from './GroupsPage';

export const GroupsModule: Module = {
  id: 'groups',
  label: 'View Groups',
  icon: UsersRound,
  path: '/groups',
  element: GroupsPage,
  requiredRole: 'admin',
  weight: 45,
};
