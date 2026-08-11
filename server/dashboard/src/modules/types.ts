import type { ComponentType } from 'react';
import type { Role } from '@/api/types';

// A `Module` is a self-contained dashboard feature: its own folder under
// `src/modules/<id>/` with an `index.ts` that exports a `Module` constant.
//
// Sidebar + router both consume the registry — adding a new feature is
// "create folder, register the module" and nothing else. See
// `dashboard/README.md` for the full style guide.
//
// There is deliberately no `icon` field. Nav rows carry a 7×7 square marker
// and the label does the work; the design system has exactly one icon in the
// chrome (the brand mark) and no per-row icon soup.
export interface Module {
  /** Stable kebab-case identifier — also used as the React key in the sidebar. */
  id: string;
  /** Human-facing label shown in the sidebar. */
  label: string;
  /** Path relative to the `/dashboard` basename (must start with `/`). */
  path: string;
  /** Top-level page rendered for this module. Owns its own internal routes. */
  element: ComponentType;
  /** Minimum role required to *see* this module in the sidebar. Default: user. */
  requiredRole?: Role;
  /**
   * Which sidebar block the entry sits in. Editorial, not derived from
   * `requiredRole`: GitHub Integration is admin-gated but belongs with the
   * day-to-day tools, while Settings is open to everyone yet reads as
   * administration.
   */
  group?: 'workspace' | 'admin';
  /** Sort order within the group — lower comes first. Default: 100. */
  weight?: number;
  /** One sentence for the home-page module grid. */
  blurb?: string;
}
