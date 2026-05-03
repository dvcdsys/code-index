import { Toaster as SonnerToaster } from 'sonner';

// Thin wrapper so the rest of the app imports `Toaster` from `@/ui/sonner`
// instead of pulling sonner directly. Keeps swap potential trivial.
export function Toaster() {
  return (
    <SonnerToaster
      position="top-right"
      richColors
      closeButton
      toastOptions={{
        classNames: {
          toast: 'rounded-lg border bg-background text-foreground',
        },
      }}
    />
  );
}

export { toast } from 'sonner';
