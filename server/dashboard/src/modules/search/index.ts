import type { Module } from '../types';
import SearchPage from './SearchPage';

export const SearchModule: Module = {
  id: 'search',
  label: 'Search',
  path: '/search',
  element: SearchPage,
  group: 'workspace',
  weight: 20,
  blurb:
    'Semantic, symbols, definitions, references and files across every project.',
};
