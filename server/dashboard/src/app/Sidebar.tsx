import { LogOut } from 'lucide-react';
import { NavLink } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { Button } from '@/ui/button';
import { cn } from '@/lib/cn';
import { MODULES } from '@/modules/registry';

// Sidebar is rendered from the module registry, filtered by the current
// user's role. A module without `requiredRole` is always visible; a module
// requiring `admin` is hidden from viewers.
//
// New features show up in the sidebar automatically once registered — no
// edits to this component are needed when a module is added.
export function Sidebar() {
  const { user, logout } = useAuth();
  const role = user?.role ?? 'viewer';

  const visible = MODULES.filter((m) => {
    if (!m.requiredRole) return true;
    if (m.requiredRole === 'viewer') return true;
    return role === 'admin';
  });

  return (
    <aside className="flex h-full w-64 flex-col border-r bg-muted/20">
      <div className="px-5 py-5">
        <span className="text-base font-semibold tracking-tight">cix</span>
        <span className="ml-2 text-xs uppercase tracking-wider text-muted-foreground">
          dashboard
        </span>
      </div>

      <nav className="flex-1 space-y-1 px-3">
        {visible.map((m) => {
          const Icon = m.icon;
          return (
            <NavLink
              key={m.id}
              to={m.path}
              end={m.path === '/'}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                  isActive
                    ? 'bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground'
                )
              }
            >
              <Icon className="h-4 w-4" />
              {m.label}
            </NavLink>
          );
        })}
      </nav>

      <div className="border-t p-3">
        {user && (
          <div className="mb-3 px-2 text-xs">
            <div className="truncate font-medium text-foreground">{user.email}</div>
            <div className="text-muted-foreground capitalize">{user.role}</div>
          </div>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start"
          onClick={() => void logout()}
        >
          <LogOut className="h-4 w-4" />
          Sign out
        </Button>
      </div>
    </aside>
  );
}
