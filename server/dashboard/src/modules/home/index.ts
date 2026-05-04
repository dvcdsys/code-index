import { Home } from 'lucide-react';
import type { Module } from '../types';
import HomePage from './HomePage';

// Landing page for /dashboard/. Renders a status strip + cards for every
// module the user can see, driven by the registry — new features show up
// here automatically.
export const HomeModule: Module = {
  id: 'home',
  label: 'Home',
  icon: Home,
  path: '/',
  element: HomePage,
  weight: 0,
};
