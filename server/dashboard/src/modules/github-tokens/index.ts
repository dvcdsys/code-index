import { Github } from 'lucide-react';
import type { Module } from '../types';
import GithubTokensPage from './GithubTokensPage';

export const GithubTokensModule: Module = {
  id: 'github-tokens',
  label: 'GitHub Tokens',
  icon: Github,
  path: '/github-tokens',
  element: GithubTokensPage,
  weight: 35,
};
