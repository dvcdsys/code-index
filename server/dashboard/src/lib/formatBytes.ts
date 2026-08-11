// Byte formatting, shared. This lived as two near-identical private copies —
// `formatBytes` in the project storage card and `formatSize` in the embedding
// model picker — which had already drifted apart on their unit ceiling (TB vs
// GB). A resource screen that reports multi-gigabyte vector stores makes that
// difference visible, so there is now one implementation.
//
// Returns an em dash for absent, zero or nonsensical values: on this dashboard
// a missing size means "the server could not measure it", and rendering that
// as "0 B" would read as "nothing there".
export function formatBytes(bytes?: number | null): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}
