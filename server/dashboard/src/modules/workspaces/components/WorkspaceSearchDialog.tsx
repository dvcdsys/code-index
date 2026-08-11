import { useRef, useState } from 'react';
import { api } from '@/api/client';
import type { components } from '@/api/generated';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import {
  Dialog,
  DialogBody,
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
        <Button variant="primary">Search workspace</Button>
      </DialogTrigger>
      {/* `min-w-0` on every direct grid child — DialogContent is
          display: grid, and grid items default to min-width: auto
          (= min-content). A long unbreakable line in the markdown
          chunk content would then blow past max-w-3xl. Letting the
          track shrink lets the inner <pre>'s overflow-x-auto actually
          kick in. */}
      <DialogContent className="max-w-3xl [&>*]:min-w-0">
        <DialogHeader>
          <DialogTitle>Search {workspace.name}</DialogTitle>
        </DialogHeader>
        <DialogBody className="min-w-0">
          <DialogDescription>
            One fan-out across every repository here. Chunks rank by raw similarity; the projects
            panel ranks repos by the mean of their top hits, capped per repo so one dominant
            project can&rsquo;t hide the rest.
          </DialogDescription>
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
            <Button variant="primary" onClick={submit} disabled={busy || query.trim() === ''}>
              {busy ? <Dots /> : null}
              Search
            </Button>
          </div>

          {err && (
            <Callout variant="danger">
              <p>{err}</p>
            </Callout>
          )}
          {resp?.pending_repos && resp.pending_repos.length > 0 && (
            <Callout variant="warn">
              <p>
                {resp.pending_repos.length} repo
                {resp.pending_repos.length === 1 ? ' is' : 's are'} still indexing — their matches
                are missing from these results.
              </p>
            </Callout>
          )}
          {resp?.failed_repos && resp.failed_repos.length > 0 && (
            <Callout variant="danger">
              <p>
                {resp.failed_repos.length} repo
                {resp.failed_repos.length === 1 ? '' : 's'} failed to answer — the results below
                are incomplete. The server log has the reason.
              </p>
            </Callout>
          )}
          {resp?.stale_fts_repos && resp.stale_fts_repos.length > 0 && (
            <Callout variant="warn">
              <p>
                {resp.stale_fts_repos.length} repo
                {resp.stale_fts_repos.length === 1 ? ' was' : 's were'} indexed before BM25
                existed, so keyword matching is empty for{' '}
                {resp.stale_fts_repos.length === 1 ? 'it' : 'them'} and ranking falls back to
                dense-only. Reindex to enable hybrid search:{' '}
                <span className="font-mono">
                  {resp.stale_fts_repos
                    .map((r) => r.project_path.split('/').pop() ?? r.project_path)
                    .join(', ')}
                </span>
              </p>
            </Callout>
          )}
          {resp && resp.status === 'empty' && (
            <Callout>
              <p>No chunks matched above the relevance threshold.</p>
            </Callout>
          )}
          {resp && resp.status === 'ok' && <SearchResults resp={resp} />}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

function SearchResults({ resp }: { resp: SearchResponse }) {
  return (
    <div className="flex max-h-[60vh] min-w-0 flex-col gap-4 overflow-y-auto">
      {resp.projects.length > 0 && (
        <div className="min-w-0">
          <span className="cix-label">Top projects</span>
          <ul className="mt-2 flex list-none flex-col gap-1.5 p-0">
            {resp.projects.map((p) => (
              <li key={p.project_path} className="min-w-0 border px-2.5 py-2">
                <div className="flex min-w-0 items-baseline justify-between gap-2">
                  <span className="min-w-0 truncate text-sm font-semibold">
                    {p.label || p.project_path}
                  </span>
                  <span className="cix-badge is-ink shrink-0 tabular-nums">
                    {p.project_score.toFixed(3)}
                  </span>
                </div>
                <div className="cix-row__meta truncate">
                  {p.num_hits} hit{p.num_hits === 1 ? '' : 's'} · bm25 {p.bm25_score.toFixed(3)} ·
                  dense {p.dense_score.toFixed(3)} · {p.project_path}
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="min-w-0">
        <span className="cix-label">Top chunks</span>
        <ul className="mt-2 flex list-none flex-col gap-2.5 p-0">
          {resp.chunks.map((c, i) => (
            <li key={i} className="min-w-0 border px-2.5 py-2">
              <div className="flex min-w-0 items-baseline justify-between gap-2">
                <span className="min-w-0 truncate font-mono text-[12.5px] font-bold">
                  {c.file_path}
                  <span className="font-normal text-muted">
                    :{c.start_line}-{c.end_line}
                  </span>
                </span>
                <span className="cix-badge shrink-0 tabular-nums">{c.score.toFixed(3)}</span>
              </div>
              <div className="cix-row__meta truncate">
                {c.project_path}
                {c.symbol_name && <span> · {c.symbol_name}</span>}
              </div>
              {/* whitespace-pre keeps the source indentation; the min-w-0
                  chain above lets overflow-x-auto scroll instead of pushing
                  the dialog past its max width. */}
              <pre className="cix-well mt-2 whitespace-pre">{c.content}</pre>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
