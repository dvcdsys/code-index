import { Settings as SettingsIcon } from 'lucide-react';
import type { Module } from '../types';
import SettingsPage from './SettingsPage';

export const SettingsModule: Module = {
  id: 'settings',
  label: 'Settings',
  icon: SettingsIcon,
  path: '/settings',
  element: SettingsPage,
  weight: 50,
};
