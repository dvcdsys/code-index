import { Status } from '@/ui/badge';
import type { TunnelState } from './types';

const TONE: Record<TunnelState, 'ok' | 'busy' | 'warn' | 'idle'> = {
  live: 'ok',
  connecting: 'warn',
  failed: 'busy',
  disabled: 'idle',
};

export function TunnelStateBadge({ state }: { state?: TunnelState }) {
  if (!state) return null;
  return (
    <Status tone={TONE[state] ?? 'idle'} className="font-mono text-[11.5px] uppercase tracking-[0.14em]">
      {state}
    </Status>
  );
}
