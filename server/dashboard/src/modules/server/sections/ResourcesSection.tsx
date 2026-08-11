import { useRef, useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import type { ReclaimCategoryId } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { Card, CardBody, CardFoot, CardHead, StatStrip } from '@/ui/card';
import { Skeleton } from '@/ui/skeleton';
import { ConfirmCleanDialog } from '../components/ConfirmCleanDialog';
import { ReclaimCategoryList } from '../components/ReclaimCategoryList';
import { useAnalyzeReclaimable, useCleanResources, useResourceUsage } from '../hooks';

// Resources answers "why is this process holding 4 GB?" and then offers to do
// something about it.
//
// The honest answer to that question is that the vector store is an in-memory
// database — chromem loads every document of every collection at startup and
// never evicts — so the heap figure and the resident document count are the
// two numbers that belong side by side here.
export function ResourcesSection() {
  const usage = useResourceUsage();
  const analyze = useAnalyzeReclaimable();
  const clean = useCleanResources();

  const [selected, setSelected] = useState<Set<ReclaimCategoryId>>(new Set());
  const [confirmOpen, setConfirmOpen] = useState(false);

  const analysis = analyze.data ?? null;

  // Seed the checkboxes from the server's defaults once per analysis. Done in
  // render behind a changed-id guard rather than in an effect, so the boxes are
  // never painted unticked for a frame before the effect corrects them.
  const seededFor = useRef<string | null>(null);
  if (analysis && seededFor.current !== analysis.analysis_id) {
    seededFor.current = analysis.analysis_id;
    setSelected(
      new Set(
        analysis.categories
          .filter((c) => c.default_selected && c.item_count > 0)
          .map((c) => c.id)
      )
    );
  }

  const busy = analyze.isPending || clean.isPending;
  const chosen = analysis ? analysis.categories.filter((c) => selected.has(c.id)) : [];
  const chosenBytes = chosen.reduce((n, c) => n + c.size_bytes, 0);

  function toggle(id: ReclaimCategoryId) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }

  function forgetAnalysis() {
    analyze.reset();
    seededFor.current = null;
    setSelected(new Set());
  }

  async function onAnalyze() {
    try {
      await analyze.mutateAsync();
    } catch (err) {
      toast.error('Could not analyze storage', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  async function onConfirm() {
    if (!analysis) return;
    try {
      const res = await clean.mutateAsync({
        analysis_id: analysis.analysis_id,
        categories: [...selected],
      });
      setConfirmOpen(false);
      forgetAnalysis();

      const failed = res.categories.reduce((n, c) => n + (c.failed_count ?? 0), 0);
      const skipped = res.categories.reduce((n, c) => n + (c.skipped_count ?? 0), 0);
      if (failed || skipped) {
        toast.warning(`Reclaimed ${formatBytes(res.reclaimed_bytes, { zero: '0 B' })}`, {
          description: `${failed} failed, ${skipped} skipped — run Analyze again to see what is left.`,
        });
      } else {
        toast.success(`Reclaimed ${formatBytes(res.reclaimed_bytes, { zero: '0 B' })}`);
      }
    } catch (err) {
      // 409 means the analysis expired or was already spent. Nothing was
      // deleted; the admin just has to look again.
      if (err instanceof ApiError && err.status === 409) {
        setConfirmOpen(false);
        forgetAnalysis();
        toast.error('That analysis expired', { description: 'Run Analyze again, then clean.' });
        return;
      }
      toast.error('Clean failed', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  const mem = usage.data?.memory;
  const vs = usage.data?.vector_store;
  const diskTotal = usage.data?.disks.reduce((n, d) => n + (d.used_bytes ?? 0), 0);

  return (
    <Card>
      <CardHead
        // Not "Resources" — the tab already says that, and a card head that
        // repeats its container tells the reader nothing. This one names what
        // the numbers below actually are.
        title="Storage & memory"
        aside={
          <Button size="sm" onClick={onAnalyze} disabled={busy}>
            {analyze.isPending ? <Dots /> : null}
            {/* Says Analyze, not Clean: this button only ever measures. The
                destructive one lives in the footer once there is something to
                act on, and a destructive feature is the last place to let a
                label disagree with the action. */}
            {analyze.isPending ? 'Analyzing' : analysis ? 'Re-analyze' : 'Analyze'}
          </Button>
        }
      />
      <CardBody className="flex flex-col gap-5">
        {usage.isLoading ? (
          <Skeleton className="h-32" />
        ) : usage.error ? (
          <Callout variant="danger">
            <b>Could not read resource usage</b>
            <p>{usage.error instanceof ApiError ? usage.error.detail : String(usage.error)}</p>
          </Callout>
        ) : (
          <>
            <StatStrip
              items={[
                {
                  label: 'Heap in use',
                  value: formatBytes(mem?.heap_alloc_bytes),
                  title: 'Live Go heap. The vector store lives here in full.',
                },
                {
                  label: mem?.rss_bytes ? 'Resident' : 'Peak resident',
                  value: formatBytes(mem?.rss_bytes ?? mem?.peak_rss_bytes),
                  title: mem?.rss_bytes
                    ? 'Current resident set size.'
                    : 'High-water mark — this platform does not expose current RSS cheaply, and a peak never falls. After a clean, watch the heap figure instead.',
                },
                {
                  label: 'Vector documents',
                  value: vs ? vs.documents.toLocaleString() : '—',
                  title: `${vs?.collections ?? 0} collections, all resident in memory`,
                },
                { label: 'Disk used', value: formatBytes(diskTotal) },
              ]}
            />

            <dl className="m-0 grid gap-x-6 gap-y-2.5 sm:grid-cols-[11rem_minmax(0,1fr)]">
              {usage.data?.disks.map((d) => (
                <div key={d.id} className="contents">
                  <dt className="text-sm text-muted">{d.label}</dt>
                  <dd className="m-0 min-w-0">
                    <div className="break-all font-mono text-[12.5px] text-ink">{d.path}</div>
                    <div className="cix-hint mt-0.5">
                      {d.exists ? formatBytes(d.used_bytes) : 'not created yet'}
                      {d.fs_free_bytes ? ` · ${formatBytes(d.fs_free_bytes)} free on volume` : ''}
                    </div>
                  </dd>
                </div>
              ))}
            </dl>
          </>
        )}

        {analyze.isPending ? (
          <p className="cix-hint m-0">
            Walking the vector store and the clone directory. On a large index this takes a few
            seconds.
          </p>
        ) : null}

        {analysis ? (
          analysis.total_reclaimable_bytes > 0 ? (
            <div className="flex flex-col gap-3.5 border-t pt-5">
              <p className="m-0 text-sm">
                <b>{formatBytes(analysis.total_reclaimable_bytes)}</b> can be reclaimed. Pick what
                to remove:
              </p>
              <ReclaimCategoryList
                categories={analysis.categories}
                selected={selected}
                onToggle={toggle}
                disabled={clean.isPending}
              />
              {analysis.warnings?.length ? (
                <Callout variant="warn">
                  <b>Worth knowing</b>
                  <ul className="m-0 mt-1 flex list-none flex-col gap-1 p-0 text-[13px]">
                    {analysis.warnings.map((w) => (
                      <li key={w}>{w}</li>
                    ))}
                  </ul>
                </Callout>
              ) : null}
            </div>
          ) : (
            <Callout variant="ok" className="mt-1">
              <b>Nothing to reclaim</b>
              <p>
                Every vector collection and cloned checkout on disk belongs to a project that still
                exists.
              </p>
            </Callout>
          )
        ) : null}
      </CardBody>

      {analysis && analysis.total_reclaimable_bytes > 0 ? (
        <CardFoot>
          <span className="cix-hint">
            {selected.size === 0
              ? 'Nothing selected'
              : `${formatBytes(chosenBytes, { zero: '0 B' })} selected across ${selected.size} categories`}
          </span>
          <Button
            variant="danger"
            className="ml-auto"
            disabled={selected.size === 0 || busy}
            onClick={() => setConfirmOpen(true)}
          >
            Clean
          </Button>
        </CardFoot>
      ) : null}

      <ConfirmCleanDialog
        open={confirmOpen}
        // Refuse to close mid-delete; the request is still running.
        onOpenChange={(next) => (!clean.isPending ? setConfirmOpen(next) : null)}
        onConfirm={onConfirm}
        isPending={clean.isPending}
        categories={chosen}
      />
    </Card>
  );
}
