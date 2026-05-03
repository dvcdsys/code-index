import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createContext, useCallback, useMemo, type ReactNode } from 'react';
import { ApiError, api } from '@/api/client';
import type { BootstrapStatusResponse, LoginRequest, LoginResponse, MeResponse, User } from '@/api/types';

// Shape exposed to components — kept narrow on purpose. Use `useAuth` to
// consume; `AuthProvider` is the only place that touches the underlying
// queries directly.
export interface AuthContextValue {
  /** True while we're still figuring out the initial auth state. */
  loading: boolean;
  /** True until the operator has done the first-run bootstrap. */
  needsBootstrap: boolean;
  /** Currently authenticated user, or null when logged out. */
  user: User | null;
  /** When true, the user must change their password before reaching the app. */
  mustChangePassword: boolean;
  /** Performs the login flow + warms /me. Throws ApiError on failure. */
  login: (req: LoginRequest) => Promise<void>;
  /** Performs server-side logout + clears cached /me. */
  logout: () => Promise<void>;
  /** Re-fetches /me — call after the user changes their password. */
  refresh: () => Promise<void>;
}

export const AuthContext = createContext<AuthContextValue | null>(null);

// Two queries drive the auth state machine:
//
//   1. /auth/bootstrap-status — public; tells us whether *any* user exists.
//      If `needs_bootstrap === true`, the dashboard renders the
//      BootstrapNeededPage instead of the login form.
//   2. /auth/me — requires a session cookie. 401 means logged out;
//      anything else is treated as an outage and surfaces the error.
export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();

  const bootstrap = useQuery({
    queryKey: ['auth', 'bootstrap-status'],
    queryFn: () => api.get<BootstrapStatusResponse>('/auth/bootstrap-status'),
    staleTime: Infinity,
    retry: false,
  });

  const me = useQuery({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      try {
        return await api.get<MeResponse>('/auth/me');
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    enabled: bootstrap.data?.needs_bootstrap === false,
    staleTime: 60_000,
    retry: false,
  });

  const loginMutation = useMutation({
    mutationFn: (req: LoginRequest) => api.post<LoginResponse>('/auth/login', req),
    onSuccess: () => {
      // /auth/me has a slightly richer envelope than /auth/login (carries
      // auth_method); re-fetching is simpler than constructing a partial.
      void qc.invalidateQueries({ queryKey: ['auth', 'me'] });
    },
  });

  const logoutMutation = useMutation({
    mutationFn: () => api.post<void>('/auth/logout'),
    onSettled: () => {
      qc.setQueryData(['auth', 'me'], null);
      qc.removeQueries({ queryKey: ['auth', 'me'] });
    },
  });

  const login = useCallback(
    async (req: LoginRequest) => {
      await loginMutation.mutateAsync(req);
    },
    [loginMutation]
  );

  const logout = useCallback(async () => {
    try {
      await logoutMutation.mutateAsync();
    } catch {
      // logout endpoint can fail if the cookie is already gone — that's fine
    }
  }, [logoutMutation]);

  const refresh = useCallback(async () => {
    await qc.invalidateQueries({ queryKey: ['auth', 'me'] });
  }, [qc]);

  const value = useMemo<AuthContextValue>(
    () => ({
      loading: bootstrap.isLoading || (bootstrap.data?.needs_bootstrap === false && me.isLoading),
      needsBootstrap: bootstrap.data?.needs_bootstrap ?? false,
      user: me.data?.user ?? null,
      mustChangePassword: me.data?.user?.must_change_password ?? false,
      login,
      logout,
      refresh,
    }),
    [bootstrap.isLoading, bootstrap.data, me.isLoading, me.data, login, logout, refresh]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
