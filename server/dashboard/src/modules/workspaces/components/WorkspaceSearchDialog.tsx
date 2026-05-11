import { useState } from 'react';
import { AlertCircle, Search } from 'lucide-react';
import { api } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
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

type SearchCommunity = {
  id: string;
  label: string;
  score: number;
  project_paths: string[];
  member_count: number;
};

type SearchChunk = {
  project_path: string;
  file_path: string;
  start_line: number;
  end_line: number;
  symbol_name?: string;
  score: number;
  community_id: string;
  community_label?: string;
  content: string;
};

type SearchResponse = {
  status: 'ok' | 'communities_not_built' | 'empty';
  communities: SearchCommunity[];
  chunks: SearchChunk[];
};

export function WorkspaceSearchDialog({ workspace }: { workspace: Workspace }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [resp, setResp] = useState<SearchResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    if (!query.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await api.get<SearchResponse>(
        `/workspaces/${workspace.id}/search`,
        { query: { q: query } },
      );
      setResp(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) {
          setResp(null);
          setQuery('');
          setErr(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="outline">
          <Search className="mr-1 size-4" />
          Search
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Search: {workspace.name}</DialogTitle>
          <DialogDescription>
            Two-stage workspace search — stage 1 routes by community
            centroid; stage 2 fans out to member repos.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
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
              Search
            </Button>
          </div>
          {err && (
            <Alert variant="destructive">
              <AlertDescription>{err}</AlertDescription>
            </Alert>
          )}
          {resp && resp.status === 'communities_not_built' && (
            <Alert>
              <AlertCircle className="size-4" />
              <AlertTitle>No centroid index yet</AlertTitle>
              <AlertDescription>
                The compute_workspace_communities job hasn't completed
                yet. Add a repo or wait ~30s after the last indexing
                finishes.
              </AlertDescription>
            </Alert>
          )}
          {resp && resp.status === 'empty' && (
            <Alert>
              <AlertDescription>No chunks matched the query.</AlertDescription>
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
    <div className="max-h-[60vh] space-y-4 overflow-y-auto">
      {resp.communities.length > 0 && (
        <div>
          <div className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            Top communities
          </div>
          <ul className="space-y-1 text-sm">
            {resp.communities.map((c) => (
              <li key={c.id} className="rounded border px-2 py-1">
                <div className="flex justify-between gap-2">
                  <span className="truncate font-medium">{c.label || '(unlabelled)'}</span>
                  <span className="font-mono text-xs">{c.score.toFixed(3)}</span>
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {c.member_count} members · {c.project_paths.join(', ')}
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div>
        <div className="mb-1 text-xs font-medium uppercase text-muted-foreground">
          Top chunks
        </div>
        <ul className="space-y-2 text-sm">
          {resp.chunks.map((c, i) => (
            <li key={i} className="rounded border px-2 py-2">
              <div className="flex justify-between gap-2 text-xs">
                <span className="truncate font-mono">
                  {c.file_path}:{c.start_line}-{c.end_line}
                </span>
                <span className="font-mono">{c.score.toFixed(3)}</span>
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {c.project_path}
                {c.symbol_name && <span> · {c.symbol_name}</span>}
              </div>
              <pre className="mt-1 overflow-x-auto rounded bg-muted/40 p-2 text-xs">
                {c.content}
              </pre>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
