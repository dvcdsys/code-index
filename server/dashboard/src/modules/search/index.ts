import { Search } from 'lucide-react';
import type { Module } from '../types';
import SearchPage from './SearchPage';

export const SearchModule: Module = {
  id: 'search',
  label: 'Search',
  icon: Search,
  path: '/search',
  element: SearchPage,
  weight: 20,
};
