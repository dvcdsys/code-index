import { useMemo } from 'react';
import type { SearchStatsSeriesResponse } from '@/api/types';

// Bars, not a line. The series is a count per fixed interval — a discrete
// quantity per bucket — and a line between two counts implies values in
// between that were never measured.
//
// The server omits empty buckets rather than sending zeroes, so the gaps have
// to be filled here before anything is drawn; plotting only the points that
// came back would compress a quiet weekend into nothing and silently rescale
// the time axis.
export function ActivityChart({ data }: { data: SearchStatsSeriesResponse }) {
  const bars = useMemo(() => {
    const { bucket_seconds: width, window_seconds: span, points } = data;
    if (width <= 0) return [];

    const byBucket = new Map<number, number>();
    for (const p of points) byBucket.set(p.bucket, p.queries);

    // Anchored to the newest bucket that exists rather than to `now`, so the
    // right edge is real data. Anchoring to the clock leaves a partial bucket
    // at the end that always reads as a drop-off.
    const last = points.length > 0 ? points[points.length - 1].bucket : Math.floor(Date.now() / 1000 / width) * width;
    const count = Math.max(1, Math.round(span / width));
    const out: Array<{ bucket: number; queries: number }> = [];
    for (let i = count - 1; i >= 0; i--) {
      const bucket = last - i * width;
      out.push({ bucket, queries: byBucket.get(bucket) ?? 0 });
    }
    return out;
  }, [data]);

  const peak = bars.reduce((m, b) => Math.max(m, b.queries), 0);

  if (peak === 0) {
    return (
      <p className="m-0 font-mono text-[11.5px] text-muted">
        No searches recorded in this window.
      </p>
    );
  }

  const fmt = new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });

  return (
    <div>
      <div className="flex h-[60px] items-end gap-px" role="img" aria-label={`Search activity, peak ${peak} queries per bucket`}>
        {bars.map((b) => (
          <span
            key={b.bucket}
            title={`${fmt.format(new Date(b.bucket * 1000))} — ${b.queries} ${b.queries === 1 ? 'query' : 'queries'}`}
            className="min-w-0 flex-1 bg-ink/70 hover:bg-ink"
            style={{
              // A bucket with traffic never renders as nothing: 1px of bar is
              // the difference between "one search" and "none", and that is the
              // distinction the chart exists to show.
              height: b.queries === 0 ? '1px' : `${Math.max(2, Math.round((b.queries / peak) * 60))}px`,
              opacity: b.queries === 0 ? 0.15 : 1,
            }}
          />
        ))}
      </div>
      <div className="flex justify-between pt-1.5 font-mono text-[10.5px] text-muted">
        <span>{bars.length > 0 ? fmt.format(new Date(bars[0].bucket * 1000)) : ''}</span>
        <span>peak {peak.toLocaleString()} / {Math.round(data.bucket_seconds / 60)}min</span>
        <span>{bars.length > 0 ? fmt.format(new Date(bars[bars.length - 1].bucket * 1000)) : ''}</span>
      </div>
    </div>
  );
}
