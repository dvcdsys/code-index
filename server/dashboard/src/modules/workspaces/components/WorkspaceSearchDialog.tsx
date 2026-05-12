import { useRef, useState } from 'react';
import { Loader2, Search } from 'lucide-react';
import { api } from '@/api/client';
import type { components } from '@/api/generated';
import { Alert, AlertDescription } from '@/ui/alert';
import { Button } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Input } from '@/ui/input';
import type { Workspace } from '../types';

// Pull the response shape straight from the OpenAPI-generated types so
// any future schema change (added fields, renamed properties) shows up
// as a TS error here instead of a silent contract drift like the one
// the boost-score refactor created.
type SearchResponse = components['schemas']['WorkspaceSearchResponse'];

export function WorkspaceSearchDialog({ workspace }: { workspace: Workspace }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [resp, setResp] = useState<SearchResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // Holding the active AbortController on a ref so a fast second
  // submit cancels the first request — without this the slower
  // response can land after the newer one and overwrite the displayed
  // results. The same ref is used to cancel on dialog close.
  const abortRef = useRef<AbortController | null>(null);

  async function submit() {
    if (!query.trim()) return;
    abortRef.current?.abort();
    const ctl = new AbortController();
    abortRef.current = ctl;
    setBusy(true);
    setErr(null);
    try {
      const r = await api.get<SearchResponse>(
        `/workspaces/${workspace.id}/search`,
        { query: { q: query }, signal: ctl.signal },
      );
      setResp(r);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      // Only flip busy off if THIS request is the active one — a
      // newer submit might have already replaced abortRef.current.
      if (abortRef.current === ctl) setBusy(false);
    }
  }

  function reset() {
    abortRef.current?.abort();
    abortRef.current = null;
    setResp(null);
    setQuery('');
    setErr(null);
    setBusy(false);
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button variant="outline">
          <Search className="mr-1 size-4" />
          Search
        </Button>
      </DialogTrigger>
      {/* `min-w-0` on every direct grid child — DialogContent is
          display: grid, and grid items default to min-width: auto
          (= min-content). A long unbreakable line in the markdown
          chunk content would then blow past max-w-3xl. Letting the
          track shrink lets the inner <pre>'s overflow-x-auto actually
          kick in. */}
      <DialogContent className="max-w-3xl [&>*]:min-w-0">
        <DialogHeader>
          <DialogTitle>Search: {workspace.name}</DialogTitle>
          <DialogDescription>
            Fan-out across every repo in this workspace. Chunks ranked by
            raw similarity score; the projects panel ranks repos by the
            mean of their top hits, capped at a few chunks per repo so a
            single dominant project can't hide the others.
          </DialogDescription>
        </DialogHeader>
        <div className="min-w-0 space-y-3">
          <div className="flex gap-2">
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !busy) void submit();
              }}
              placeholder="e.g. JWT validation across services"
            />
            <Button onClick={submit} disabled={busy || query.trim() === ''}>
              {busy ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : null}
              Search
            </Button>
          </div>
          {err && (
            <Alert variant="destructive">
              <AlertDescription>{err}</AlertDescription>
            </Alert>
          )}
          {resp?.pending_repos && resp.pending_repos.length > 0 && (
            <Alert>
              <AlertDescription>
                {resp.pending_repos.length} repo
                {resp.pending_repos.length === 1 ? '' : 's'} still
                indexing — their matches won't appear yet.
              </AlertDescription>
            </Alert>
          )}
          {resp?.failed_repos && resp.failed_repos.length > 0 && (
            <Alert variant="destructive">
              <AlertDescription>
                {resp.failed_repos.length} repo
                {resp.failed_repos.length === 1 ? '' : 's'} failed to
                query — results below are incomplete. Check server logs
                for details.
              </AlertDescription>
            </Alert>
          )}
          {resp && resp.status === 'empty' && (
            <Alert>
              <AlertDescription>
                No chunks matched the query above the relevance threshold.
              </AlertDescription>
            </Alert>
          )}
          {resp && resp.status === 'ok' && <SearchResults resp={resp} />}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SearchResults({ resp }: { resp: SearchResponse }) {
  return (
    <div className="max-h-[60vh] min-w-0 space-y-4 overflow-y-auto">
      {resp.projects.length > 0 && (
        <div className="min-w-0">
          <div className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            Top projects
          </div>
          <ul className="space-y-1 text-sm">
            {resp.projects.map((p) => (
              <li
                key={p.project_path}
                className="min-w-0 rounded border px-2 py-1"
              >
                <div className="flex min-w-0 justify-between gap-2">
                  <span className="min-w-0 truncate font-medium">
                    {p.label || p.project_path}
                  </span>
                  <span className="shrink-0 font-mono text-xs">
                    {p.project_score.toFixed(3)}
                  </span>
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {p.num_hits} hit{p.num_hits === 1 ? '' : 's'} ·{' '}
                  bm25 {p.bm25_score.toFixed(3)} · dense{' '}
                  {p.dense_score.toFixed(3)} · {p.project_path}
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="min-w-0">
        <div className="mb-1 text-xs font-medium uppercase text-muted-foreground">
          Top chunks
        </div>
        <ul className="space-y-2 text-sm">
          {resp.chunks.map((c, i) => (
            <li key={i} className="min-w-0 rounded border px-2 py-2">
              <div className="flex min-w-0 justify-between gap-2 text-xs">
                <span className="min-w-0 truncate font-mono">
                  {c.file_path}:{c.start_line}-{c.end_line}
                </span>
                <span className="shrink-0 font-mono">
                  {c.score.toFixed(3)}
                </span>
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {c.project_path}
                {c.symbol_name && <span> · {c.symbol_name}</span>}
              </div>
              {/* whitespace-pre keeps source indentation; the parent
                  min-w-0 chain lets overflow-x-auto produce a scrollbar
                  instead of pushing the dialog wider than max-w-3xl. */}
              <pre className="mt-1 max-w-full overflow-x-auto rounded bg-muted/40 p-2 text-xs whitespace-pre">
                {c.content}
              </pre>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
