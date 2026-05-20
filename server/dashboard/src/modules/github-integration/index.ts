import { Github } from 'lucide-react';
import type { Module } from '../types';
import GithubIntegrationPage from './GithubIntegrationPage';

export const GithubIntegrationModule: Module = {
  id: 'github-integration',
  label: 'GitHub Integration',
  icon: Github,
  path: '/github-integration',
  element: GithubIntegrationPage,
  weight: 35,
};
