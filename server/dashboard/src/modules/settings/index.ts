import type { Module } from '../types';
import SettingsPage from './SettingsPage';

export const SettingsModule: Module = {
  id: 'settings',
  label: 'Settings',
  path: '/settings',
  element: SettingsPage,
  group: 'admin',
  weight: 50,
  blurb:
    'Your own preferences — theme, editor protocol, password, sessions.',
};
