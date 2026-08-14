import { NavLink } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { cn } from '@/lib/cn';
import { visibleModules } from '@/modules/registry';
import { BrandMark } from './BrandMark';

// Rendered straight from the module registry, filtered by role, split into
// two blocks: the day-to-day tools and an ADMIN group under a mono rule.
//
// Rows carry a 7×7 square marker instead of an icon — accent on the active
// row, quiet otherwise. The label carries the meaning; the marker only says
// "you are here".
export function Sidebar() {
  const { user, logout } = useAuth();
  const modules = visibleModules(user?.role);
  const workspace = modules.filter((m) => m.group !== 'admin');
  const admin = modules.filter((m) => m.group === 'admin');

  const initials = (user?.email ?? '??').slice(0, 2).toUpperCase();

  return (
    <aside className="cix-sidebar">
      <div className="flex flex-none items-center gap-2.5 border-b p-4 font-mono font-bold tracking-[0.08em]">
        <BrandMark />
        <span>cix</span>
        <span className="text-[11px] font-normal tracking-[0.16em] text-muted">DASHBOARD</span>
      </div>

      <nav className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto p-2.5">
        {workspace.map((m) => (
          <NavLink
            key={m.id}
            to={m.path}
            end={m.path === '/'}
            className={({ isActive }) => cn('cix-nav-item', isActive && 'is-active')}
          >
            {m.label}
          </NavLink>
        ))}

        {admin.length > 0 && (
          <>
            <div className="mx-2.5 mb-1 mt-3.5 border-t border-line-quiet" />
            <span className="cix-nav-group">ADMIN</span>
            {admin.map((m) => (
              <NavLink
                key={m.id}
                to={m.path}
                className={({ isActive }) => cn('cix-nav-item', isActive && 'is-active')}
              >
                {m.label}
              </NavLink>
            ))}
          </>
        )}
      </nav>

      {user && (
        <div className="flex flex-none items-center gap-2.5 border-t px-3.5 py-3">
          <span
            aria-hidden
            className="flex h-7 w-7 flex-none items-center justify-center bg-ink font-mono text-[11px] font-bold text-surface"
          >
            {initials}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[13px] font-semibold" title={user.email}>
              {user.email}
            </span>
            <span className="block font-mono text-[11px] text-muted">{user.role}</span>
          </span>
          <button
            type="button"
            onClick={() => void logout()}
            title="Sign out"
            aria-label="Sign out"
            className="flex h-7 w-7 flex-none items-center justify-center font-mono text-sm text-muted hover:bg-warm-deep hover:text-ink"
          >
            ⇥
          </button>
        </div>
      )}
    </aside>
  );
}
