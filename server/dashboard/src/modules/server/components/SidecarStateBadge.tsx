import { Badge } from '@/ui/badge';

const VARIANT: Record<string, 'default' | 'secondary' | 'destructive' | 'outline'> = {
  running: 'default',
  starting: 'secondary',
  restarting: 'secondary',
  failed: 'destructive',
  disabled: 'outline',
};

export function SidecarStateBadge({ state }: { state?: string }) {
  if (!state) return null;
  return (
    <Badge variant={VARIANT[state] ?? 'outline'} className="capitalize">
      {state}
    </Badge>
  );
}
