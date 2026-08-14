import { Badge } from '@/ui/badge';

// Provenance is the least important thing in a field, so it gets the least
// weight. `db` used to render as a solid ink chip, which made the quietest
// fact on the card the loudest mark on it — the whole form read as speckled.
// Outline for db (the one the admin owns and can change), quiet for the two
// that are merely reported. The words already carry the meaning; the styling
// only has to rank them.
const VARIANT: Record<string, 'outline' | 'quiet'> = {
  db: 'outline',
  env: 'quiet',
  recommended: 'quiet',
};

const LABEL: Record<string, string> = {
  db: 'DB',
  env: 'ENV',
  recommended: 'DEFAULT',
};

// Where the currently-effective value came from: DB (this dashboard saved an
// override), ENV (the operator set CIX_* at boot), DEFAULT (falling through to
// the hardcoded recommendation).
export function SourcePill({ source }: { source?: string }) {
  if (!source) return null;
  return <Badge variant={VARIANT[source] ?? 'quiet'}>{LABEL[source] ?? source}</Badge>;
}
