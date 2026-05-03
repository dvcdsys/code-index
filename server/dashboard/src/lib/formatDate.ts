// Centralised date formatters so the whole UI stays consistent.
//
// All cix-server APIs emit RFC3339 strings (Go time.Time JSON default) or
// `null` for never-touched fields like `last_used_at`. Helpers here accept
// `string | null | undefined` to keep call sites concise.

const DATE_FMT = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
});

const TIME_FMT = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
});

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return DATE_FMT.format(d);
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${DATE_FMT.format(d)}, ${TIME_FMT.format(d)}`;
}

const RTF = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

const STEPS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ['year', 60 * 60 * 24 * 365],
  ['month', 60 * 60 * 24 * 30],
  ['day', 60 * 60 * 24],
  ['hour', 60 * 60],
  ['minute', 60],
  ['second', 1],
];

export function formatRelative(iso: string | null | undefined): string {
  if (!iso) return 'never';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return iso;
  const seconds = Math.round((then - Date.now()) / 1000);
  for (const [unit, secs] of STEPS) {
    if (Math.abs(seconds) >= secs || unit === 'second') {
      return RTF.format(Math.round(seconds / secs), unit);
    }
  }
  return 'just now';
}
