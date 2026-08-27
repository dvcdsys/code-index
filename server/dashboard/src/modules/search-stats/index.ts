import type { Module } from '../types';
import SearchStatsPage from './SearchStatsPage';

export const SearchStatsModule: Module = {
  id: 'search-stats',
  label: 'Search statistics',
  path: '/search-stats',
  element: SearchStatsPage,
  // Not admin-gated: the page is scoped to the projects the viewer can already
  // search, so it tells them nothing they could not learn by searching. An
  // admin sees every project; everyone else sees their own.
  group: 'workspace',
  // Sits directly after the two surfaces it reports on — Search and
  // Workspaces — rather than with the admin tools, because it is read by
  // whoever is doing the searching.
  weight: 26,
  blurb: 'Which projects get searched, and which files keep coming back.',
};
