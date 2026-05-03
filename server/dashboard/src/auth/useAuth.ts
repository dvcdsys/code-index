import { useContext } from 'react';
import { AuthContext, type AuthContextValue } from './AuthProvider';

// Hook for components to read the current auth state. Throws when used
// outside <AuthProvider> — that's a developer mistake, not a runtime
// condition we'd want to silently fall through.
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within <AuthProvider>');
  }
  return ctx;
}
