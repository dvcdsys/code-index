import { Loader2 } from 'lucide-react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import BootstrapNeededPage from '@/auth/BootstrapNeededPage';
import ChangePasswordPage from '@/auth/ChangePasswordPage';
import LoginPage from '@/auth/LoginPage';
import { MODULES } from '@/modules/registry';
import { Shell } from './Shell';

// Top-level auth + route gate. Three states branch off here:
//   - bootstrap not done       → BootstrapNeededPage (no other route works)
//   - logged out               → LoginPage          (no Shell, no nav)
//   - must change password     → ChangePasswordPage (no Shell, no nav)
//   - logged in & happy        → Shell + module routes
//
// Module routes are derived from the registry — no manual <Route> entries
// per feature. Each module owns its `path` (relative to /dashboard) and
// renders whatever it likes inside.
export default function App() {
  const { loading, needsBootstrap, user, mustChangePassword } = useAuth();

  if (loading) {
    return (
      <div className="flex min-h-dvh items-center justify-center text-muted-foreground">
        <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        Loading…
      </div>
    );
  }

  if (needsBootstrap) return <BootstrapNeededPage />;

  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  if (mustChangePassword) {
    return (
      <Routes>
        <Route path="/change-password" element={<ChangePasswordPage />} />
        <Route path="*" element={<Navigate to="/change-password" replace />} />
      </Routes>
    );
  }

  // Authenticated + ready — render every registered module under the Shell.
  // A module whose role gate excludes the current user simply has no <Route>
  // mounted, so a deep link to it 404s back to /.
  const visible = MODULES.filter((m) => {
    if (!m.requiredRole) return true;
    if (m.requiredRole === 'viewer') return true;
    return user.role === 'admin';
  });

  return (
    <Shell>
      <Routes>
        {visible.map((m) => {
          // Modules can own a sub-tree by defining their own routes inside
          // their element; we mount with a trailing wildcard so they get
          // them on `/<path>/*`.
          const mountPath = m.path === '/' ? '/*' : `${m.path}/*`;
          const Element = m.element;
          return <Route key={m.id} path={mountPath} element={<Element />} />;
        })}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  );
}
