import { Badge } from '@/ui/badge';
import type { TunnelState } from './types';

const VARIANT: Record<TunnelState, 'default' | 'secondary' | 'destructive' | 'outline'> = {
  live: 'default',
  connecting: 'secondary',
  failed: 'destructive',
  disabled: 'outline',
};

export function TunnelStateBadge({ state }: { state?: TunnelState }) {
  if (!state) return null;
  return (
    <Badge variant={VARIANT[state] ?? 'outline'} className="capitalize">
      {state}
    </Badge>
  );
}
