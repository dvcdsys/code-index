// Byte formatting, shared. This lived as two near-identical private copies —
// `formatBytes` in the project storage card and `formatSize` in the embedding
// model picker — which had already drifted apart on their unit ceiling (TB vs
// GB). A resource screen that reports multi-gigabyte vector stores makes that
// difference visible, so there is now one implementation.
//
// Returns an em dash for absent or nonsensical values: on this dashboard a
// missing size means "the server could not measure it", and rendering that as
// "0 B" would read as "nothing there".
//
// Zero is the interesting case and depends on what is being shown. For a
// measured size, 0 and "unmeasurable" are practically the same and the dash is
// right. For a TOTAL — bytes selected, bytes reclaimed — zero is a real answer,
// and "Reclaimed —" reads like the operation failed. Pass zero: '0 B' there.
export function formatBytes(
  bytes?: number | null,
  opts?: { zero?: string }
): string {
  if (bytes == null || !Number.isFinite(bytes)) return '—';
  if (bytes <= 0) return opts?.zero ?? '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}
