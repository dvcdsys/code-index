import { Toaster as SonnerToaster } from 'sonner';

// Toasts are ink slabs with cream text and an accent hard shadow — the one
// place the shadow is coloured, because a toast is the only surface that
// floats over the whole app.
export function Toaster() {
  return (
    <SonnerToaster
      position="top-right"
      closeButton
      // richColors would repaint the toast with its own green/red palette;
      // the accent shadow plus the status square already carry severity.
      richColors={false}
      toastOptions={{
        unstyled: false,
        classNames: {
          toast:
            'cix-toast !rounded-none !border-[1.5px] !border-ink !bg-ink !text-surface ' +
            '!shadow-hard-accent !font-sans !text-sm !gap-3',
          title: '!font-semibold !text-surface',
          description: '!text-[13px] !text-[rgb(var(--c-line-quiet))]',
          actionButton: '!bg-surface !text-ink !rounded-none !font-semibold',
          cancelButton: '!bg-transparent !text-surface !rounded-none',
          closeButton:
            '!rounded-none !border-[1.5px] !border-ink !bg-surface !text-ink hover:!bg-warm',
          icon: '!hidden',
        },
      }}
    />
  );
}

export { toast } from 'sonner';
