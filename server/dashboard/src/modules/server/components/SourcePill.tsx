import { Badge } from '@/ui/badge';

const VARIANT: Record<string, 'default' | 'secondary' | 'outline'> = {
  db: 'default',
  env: 'secondary',
  recommended: 'outline',
};

const LABEL: Record<string, string> = {
  db: 'DB',
  env: 'Env',
  recommended: 'Default',
};

// SourcePill renders a tiny "DB" / "Env" / "Default" tag next to a runtime
// config field, telling the admin where the currently-effective value came
// from. "DB" means the dashboard saved an override; "Env" means the operator
// set CIX_* at boot; "Default" means we're falling through to the hardcoded
// recommended value.
export function SourcePill({ source }: { source?: string }) {
  if (!source) return null;
  return (
    <Badge variant={VARIANT[source] ?? 'outline'} className="text-[10px] uppercase tracking-wide">
      {LABEL[source] ?? source}
    </Badge>
  );
}
