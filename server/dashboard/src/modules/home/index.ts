import { Home } from 'lucide-react';
import type { Module } from '../types';
import HomePage from './HomePage';

// Placeholder Home module so the sidebar isn't empty in PR-B. Will be
// replaced or augmented by Projects + Search modules in PR-C.
export const HomeModule: Module = {
  id: 'home',
  label: 'Home',
  icon: Home,
  path: '/',
  element: HomePage,
  weight: 0,
};
