import type { ComponentType, SVGProps } from 'react';
import type { Role } from '@/api/types';

// A `Module` is a self-contained dashboard feature: its own folder under
// `src/modules/<id>/` with an `index.ts` that exports a `Module` constant.
//
// Sidebar + router both consume the registry — adding a new feature is
// "create folder, register the module" and nothing else. See
// `dashboard/README.md` for the full style guide.
export interface Module {
  /** Stable kebab-case identifier — also used as the React key in the sidebar. */
  id: string;
  /** Human-facing label shown in the sidebar. */
  label: string;
  /** Sidebar icon — must accept `className`. lucide-react icons satisfy this. */
  icon: ComponentType<SVGProps<SVGSVGElement>>;
  /** Path relative to the `/dashboard` basename (must start with `/`). */
  path: string;
  /** Top-level page rendered for this module. Owns its own internal routes. */
  element: ComponentType;
  /** Minimum role required to *see* this module in the sidebar. Default: viewer. */
  requiredRole?: Role;
  /** Sort order in the sidebar — lower comes first. Default: 100. */
  weight?: number;
}
