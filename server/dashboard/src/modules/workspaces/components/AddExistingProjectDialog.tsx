import { useEffect, useMemo, useState } from 'react';
import { api, ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Badge, StatusDot } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import { Checkbox } from '@/ui/checkbox';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Field, Input } from '@/ui/input';
import type { Project, ProjectListResponse } from '@/api/types';

// Per-row disabled reason. null means the row is selectable. Local
// projects are now first-class linkable rows — only "not yet indexed"
// disables them.
type LinkDisabledReason = 'not-indexed';

function disabledReasonFor(p: Project): LinkDisabledReason | null {
  if (p.status !== 'indexed') return 'not-indexed';
  return null;
}

function disabledLabel(r: LinkDisabledReason): string {
  switch (r) {
    case 'not-indexed':
      return 'not indexed yet';
  }
}

// AddExistingProjectDialog lets the operator pick one or many already-
// indexed projects and link them into this workspace in a single
// submission. The list shows every project on the server — unselectable
// rows are rendered as disabled with a short reason so the operator
// understands why they can't be picked.
//
// Submit fans out N POSTs to /workspaces/{id}/projects sequentially.
// We chose sequential over parallel because: (a) it makes per-project
// error reporting trivial, (b) the per-call cost is tiny (no clone, no
// index), (c) a backend hiccup mid-batch leaves the workspace in a
// predictable partial state instead of a thundering-herd race.
export function AddExistingProjectDialog({
  workspaceID,
  existingProjectPaths,
  onAdded,
}: {
  workspaceID: string;
  existingProjectPaths: string[];
  onAdded: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [submitting, setSubmitting] = useState(false);
  // Per-project failure messages collected during the batch submit.
  // Keyed by path_hash so we can render them next to the row.
  const [errs, setErrs] = useState<Record<string, string>>({});

  // Fetch projects when the dialog opens. Resetting state on each open
  // means the user gets a fresh picker every time — selections from a
  // previous open don't leak through.
  useEffect(() => {
    if (!open) return;
    setProjects(null);
    setLoadErr(null);
    setQuery('');
    setSelected(new Set());
    setErrs({});
    api
      .get<ProjectListResponse>('/projects')
      .then((r) => setProjects(r.projects))
      .catch((e: unknown) => {
        const msg =
          e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e);
        setLoadErr(msg);
        setProjects([]);
      });
  }, [open]);

  const inWorkspace = useMemo(() => new Set(existingProjectPaths), [existingProjectPaths]);

  // Annotate each project with its disabled reason once, so render +
  // filter + count all share one source of truth. Projects already in
  // this workspace are dropped entirely — they're not useful targets
  // for the "Add Existing Project" flow.
  const annotated = useMemo(() => {
    if (!projects) return [];
    return projects
      .filter((p) => !inWorkspace.has(p.host_path))
      .map((p) => ({ p, reason: disabledReasonFor(p) }));
  }, [projects, inWorkspace]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return annotated;
    return annotated.filter((row) => row.p.host_path.toLowerCase().includes(q));
  }, [annotated, query]);

  const selectableInView = useMemo(
    () => filtered.filter((row) => row.reason === null),
    [filtered],
  );
  const allInViewSelected =
    selectableInView.length > 0 &&
    selectableInView.every((row) => selected.has(row.p.path_hash));

  function toggle(hash: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(hash)) next.delete(hash);
      else next.add(hash);
      return next;
    });
  }

  function toggleAllInView() {
    setSelected((prev) => {
      const next = new Set(prev);
      if (allInViewSelected) {
        for (const row of selectableInView) next.delete(row.p.path_hash);
      } else {
        for (const row of selectableInView) next.add(row.p.path_hash);
      }
      return next;
    });
  }

  async function handleSubmit() {
    if (selected.size === 0 || !projects) return;
    setSubmitting(true);
    const collected: Record<string, string> = {};
    const succeeded = new Set<string>();
    const toLink = projects.filter((p) => selected.has(p.path_hash));
    for (const p of toLink) {
      try {
        await api.post(
          `/workspaces/${workspaceID}/projects`,
          { project_hash: p.path_hash },
        );
        succeeded.add(p.path_hash);
      } catch (e: unknown) {
        const msg =
          e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e);
        collected[p.path_hash] = msg;
      }
    }
    setErrs(collected);
    setSubmitting(false);

    // Drop the successfully-linked projects from the local list so
    // they disappear immediately, without waiting for the parent's
    // repos refetch to round-trip and reflow our existingProjectPaths
    // prop. Also clear them from the selected set so the count drops
    // back to "0 selected" (or to the count of still-failing rows).
    if (succeeded.size > 0) {
      setProjects((prev) => (prev ? prev.filter((p) => !succeeded.has(p.path_hash)) : prev));
      setSelected((prev) => {
        const next = new Set(prev);
        for (const h of succeeded) next.delete(h);
        return next;
      });
    }

    if (Object.keys(collected).length === 0) {
      // All succeeded — close + refresh the parent list.
      setOpen(false);
      onAdded();
    } else if (succeeded.size > 0) {
      // Partial success — refresh the parent so the successes show up,
      // keep the dialog open with the per-row error annotations so the
      // operator can see what failed without losing context.
      onAdded();
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>Link project</Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Link existing projects</DialogTitle>
        </DialogHeader>

        <DialogBody>
          <DialogDescription>
            Pick one or more indexed projects. They join workspace search without being cloned or
            indexed again.
          </DialogDescription>

          {loadErr && (
            <Callout variant="danger">
              <b>Could not load projects</b>
              <p>{loadErr}</p>
            </Callout>
          )}

          <Field label="Filter" htmlFor="proj-filter">
            <Input
              id="proj-filter"
              placeholder="github.com/owner/repo or /local/path"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              disabled={projects === null}
            />
          </Field>

          <div className="flex items-center justify-between gap-3 font-mono text-[11.5px] text-muted">
            <span>
              {projects === null
                ? 'loading…'
                : `${selected.size} selected · ${selectableInView.length} selectable here · ${annotated.length} total`}
            </span>
            {selectableInView.length > 0 && (
              <button
                type="button"
                onClick={toggleAllInView}
                className="text-accent hover:underline"
                disabled={submitting}
              >
                {allInViewSelected ? 'clear visible' : 'select all visible'}
              </button>
            )}
          </div>

          <div className="max-h-80 overflow-y-auto border">
            {projects === null ? (
              <div className="flex items-center justify-center gap-2 py-6 font-mono text-[12px] text-muted">
                <Dots /> loading projects…
              </div>
            ) : filtered.length === 0 ? (
              <div className="px-3 py-6 text-center text-sm text-dim">
                {annotated.length === 0
                  ? 'No projects on this server yet.'
                  : 'Nothing matches the filter.'}
              </div>
            ) : (
              <ul>
                {filtered.map(({ p, reason }) => {
                  const isChecked = selected.has(p.path_hash);
                  const disabled = reason !== null || submitting;
                  const rowErr = errs[p.path_hash];
                  return (
                    <li key={p.path_hash} className="border-b border-line-soft last:border-b-0">
                      <label
                        className={`flex items-start gap-3 px-3 py-2.5 text-sm ${
                          disabled
                            ? 'cursor-not-allowed text-faint'
                            : 'cursor-pointer hover:bg-surface-hover'
                        } ${isChecked ? 'bg-surface-hover' : ''}`}
                      >
                        <Checkbox
                          className="mt-0.5"
                          checked={isChecked}
                          disabled={disabled}
                          onChange={() => toggle(p.path_hash)}
                        />
                        <div className="min-w-0 flex-1">
                          <div className="truncate font-mono text-[13px]" title={p.host_path}>
                            {p.host_path}
                          </div>
                          <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-[11px] text-muted">
                            <span className="inline-flex items-center gap-1.5">
                              <StatusDot
                                tone={
                                  p.status === 'indexed'
                                    ? 'ok'
                                    : p.status === 'error'
                                      ? 'busy'
                                      : 'warn'
                                }
                              />
                              {p.status}
                            </span>
                            {p.languages.slice(0, 3).map((l) => (
                              <span key={l}>{l}</span>
                            ))}
                            {reason && <Badge variant="warn">{disabledLabel(reason)}</Badge>}
                          </div>
                          {rowErr && (
                            <div className="mt-1 font-mono text-[11px] text-accent">
                              link failed: {rowErr}
                            </div>
                          )}
                        </div>
                      </label>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </DialogBody>

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={handleSubmit}
            disabled={selected.size === 0 || submitting}
          >
            {submitting ? <Dots /> : null}
            {selected.size === 0 ? 'Link selected' : `Link ${selected.size} selected`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

