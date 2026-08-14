import { Navigate, Route, Routes } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import BootstrapNeededPage from '@/auth/BootstrapNeededPage';
import ChangePasswordPage from '@/auth/ChangePasswordPage';
import LoginPage from '@/auth/LoginPage';
import { visibleModules } from '@/modules/registry';
import { Dots } from '@/ui/button';
import { Shell } from './Shell';
import { StatusFactProvider } from './StatusBar';

// Top-level auth + route gate. Four states branch off here:
//   - bootstrap not done    → BootstrapNeededPage (no other route works)
//   - logged out            → LoginPage           (no Shell, no nav)
//   - must change password  → ChangePasswordPage  (no Shell, no nav)
//   - logged in & happy     → Shell + module routes
//
// Module routes come from the registry — no manual <Route> per feature. A
// module whose role gate excludes the current user has no <Route> mounted at
// all, so a deep link to it falls through to "/".
export default function App() {
  const { loading, needsBootstrap, user, mustChangePassword } = useAuth();

  if (loading) {
    return (
      <div className="flex min-h-dvh items-center justify-center gap-3 font-mono text-[13px] text-muted">
        <Dots />
        loading
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

  return (
    <StatusFactProvider>
      <Shell>
        <Routes>
          {visibleModules(user.role).map((m) => {
            // Modules can own a sub-tree, so each mounts with a trailing
            // wildcard and routes `/<path>/*` internally.
            const mountPath = m.path === '/' ? '/*' : `${m.path}/*`;
            const Element = m.element;
            return <Route key={m.id} path={mountPath} element={<Element />} />;
          })}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Shell>
    </StatusFactProvider>
  );
}
