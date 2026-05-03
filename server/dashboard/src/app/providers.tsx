import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Toaster } from '@/ui/sonner';
import { TooltipProvider } from '@/ui/tooltip';
import { AuthProvider } from '@/auth/AuthProvider';
import { ApiError } from '@/api/client';
import { ThemeProvider } from './ThemeProvider';

// One place to wire app-level providers — order matters:
//   1. QueryClient: needed before AuthProvider, which uses useQuery for /me.
//   2. AuthProvider: hooks the whole app to the current session.
//   3. Toaster: rendered last so toasts paint above everything else.
export function AppProviders({ children }: { children: ReactNode }) {
  // Lazy-init so a fast refresh doesn't lose in-flight queries.
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            gcTime: 5 * 60_000,
            refetchOnWindowFocus: false,
            // Default retry: 3 fast retries, but not on auth errors — those
            // mean the cookie is gone, retrying just delays the redirect.
            retry: (failureCount, error) => {
              if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
                return false;
              }
              return failureCount < 2;
            },
          },
          mutations: {
            // Mutations should never be auto-retried; the user clicked once,
            // surface the failure once.
            retry: false,
          },
        },
      })
  );

  return (
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <TooltipProvider delayDuration={200}>
          <AuthProvider>{children}</AuthProvider>
          <Toaster />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );
}
