import { Status } from '@/ui/badge';

const TONE: Record<string, 'ok' | 'busy' | 'warn' | 'idle'> = {
  running: 'ok',
  starting: 'warn',
  restarting: 'warn',
  failed: 'busy',
  disabled: 'idle',
};

// A square plus the state word — the state is never carried by colour alone.
export function SidecarStateBadge({ state }: { state?: string }) {
  if (!state) return null;
  return (
    <Status tone={TONE[state] ?? 'idle'} className="font-mono text-[11.5px] uppercase tracking-[0.14em]">
      {state}
    </Status>
  );
}
