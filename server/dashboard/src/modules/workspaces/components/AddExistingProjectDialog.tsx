import { useEffect, useMemo, useState } from 'react';
import { Link2, Loader2 } from 'lucide-react';
import { api, ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
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
// Submit fans out N POSTs to /workspaces/{id}/repos/link sequentially.
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
        <Button variant="outline">
          <Link2 className="mr-1 size-4" /> Add Existing Project
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Link existing projects</DialogTitle>
          <DialogDescription>
            Select one or more indexed projects to link into this workspace.
            Linked projects show up in workspace search without re-cloning or
            re-indexing.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {loadErr && (
            <Alert variant="destructive">
              <AlertTitle>Could not load projects</AlertTitle>
              <AlertDescription>{loadErr}</AlertDescription>
            </Alert>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="proj-filter">Filter</Label>
            <Input
              id="proj-filter"
              placeholder="github.com/owner/repo or /local/path"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              disabled={projects === null}
            />
          </div>

          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>
              {projects === null
                ? 'Loading…'
                : `${selected.size} selected · ${selectableInView.length} selectable in view · ${annotated.length} total`}
            </span>
            {selectableInView.length > 0 && (
              <button
                type="button"
                onClick={toggleAllInView}
                className="font-medium text-primary hover:underline"
                disabled={submitting}
              >
                {allInViewSelected ? 'Clear visible' : 'Select all visible'}
              </button>
            )}
          </div>

          <div className="max-h-80 overflow-y-auto rounded border">
            {projects === null ? (
              <div className="flex items-center justify-center gap-2 py-6 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" /> Loading projects…
              </div>
            ) : filtered.length === 0 ? (
              <div className="px-3 py-6 text-center text-sm text-muted-foreground">
                {annotated.length === 0
                  ? 'No projects on this server yet.'
                  : 'No projects match the filter.'}
              </div>
            ) : (
              <ul className="divide-y">
                {filtered.map(({ p, reason }) => {
                  const isChecked = selected.has(p.path_hash);
                  const disabled = reason !== null || submitting;
                  const rowErr = errs[p.path_hash];
                  return (
                    <li key={p.path_hash}>
                      <label
                        className={`flex cursor-pointer items-start gap-3 px-3 py-2 text-sm hover:bg-muted ${
                          disabled ? 'cursor-not-allowed opacity-60 hover:bg-transparent' : ''
                        } ${isChecked ? 'bg-muted/50' : ''}`}
                      >
                        <input
                          type="checkbox"
                          className="mt-0.5 size-4 shrink-0 cursor-pointer accent-primary disabled:cursor-not-allowed"
                          checked={isChecked}
                          disabled={disabled}
                          onChange={() => toggle(p.path_hash)}
                        />
                        <div className="min-w-0 flex-1 space-y-0.5">
                          <div
                            className="truncate font-mono text-xs"
                            title={p.host_path}
                          >
                            {p.host_path}
                          </div>
                          <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                            <Badge
                              variant={
                                p.status === 'indexed'
                                  ? 'default'
                                  : p.status === 'error'
                                  ? 'destructive'
                                  : 'secondary'
                              }
                              className="text-[10px]"
                            >
                              {p.status}
                            </Badge>
                            {p.languages.slice(0, 3).map((l) => (
                              <span key={l}>{l}</span>
                            ))}
                            {reason && (
                              <span className="font-medium text-amber-700 dark:text-amber-400">
                                · {disabledLabel(reason)}
                              </span>
                            )}
                          </div>
                          {rowErr && (
                            <div className="mt-1 text-[11px] text-destructive">
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
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => setOpen(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={selected.size === 0 || submitting}>
            {submitting ? <Loader2 className="mr-1 size-4 animate-spin" /> : null}
            {selected.size === 0
              ? 'Link selected'
              : `Link ${selected.size} selected`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

