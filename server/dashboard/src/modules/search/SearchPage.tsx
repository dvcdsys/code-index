import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { AlertCircle, FileQuestion, Search as SearchIcon } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import { Skeleton } from '@/ui/skeleton';
import { Tabs, TabsList, TabsTrigger } from '@/ui/tabs';
import { SearchInput } from './components/SearchInput';
import {
  LimitInput,
  LanguagesInput,
  MinScoreSlider,
  ModeSpecificHelp,
  ProjectPicker,
} from './components/Filters';
import { ResultFileCard } from './components/ResultFileCard';
import { OpenInEditorButton } from './components/ResultSnippet';
import {
  SEARCH_MODES,
  type SearchMode,
  useDefinitions,
  useFileSearch,
  useReferences,
  useSemanticSearch,
  useSymbolSearch,
} from './hooks';

const MODE_IDS = SEARCH_MODES.map((m) => m.id) as readonly SearchMode[];

function isMode(value: string | null): value is SearchMode {
  return value !== null && (MODE_IDS as readonly string[]).includes(value);
}

export default function SearchPage() {
  const [params, setParams] = useSearchParams();
  const mode = isMode(params.get('mode')) ? (params.get('mode') as SearchMode) : 'semantic';
  const projectHash = params.get('project') ?? undefined;
  const queryParam = params.get('q') ?? '';
  const [draft, setDraft] = useState(queryParam);

  // Debounce: input → URL after 250ms idle. Enter on the form bypasses this
  // via `commitQuery(draft)`.
  useEffect(() => {
    const id = setTimeout(() => {
      if (draft === queryParam) return;
      const next = new URLSearchParams(params);
      if (draft.trim()) next.set('q', draft);
      else next.delete('q');
      setParams(next, { replace: true });
    }, 250);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft]);

  // Sync local draft if URL changes externally (e.g. user pastes a link).
  useEffect(() => {
    setDraft(queryParam);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryParam]);

  function commitQuery(value: string) {
    const v = value.trim();
    const next = new URLSearchParams(params);
    if (v) next.set('q', v);
    else next.delete('q');
    setParams(next, { replace: true });
  }

  function setMode(next: SearchMode) {
    const p = new URLSearchParams(params);
    p.set('mode', next);
    setParams(p, { replace: true });
  }
  function setProject(hash: string) {
    const p = new URLSearchParams(params);
    p.set('project', hash);
    setParams(p, { replace: true });
  }

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Search</h1>
        <p className="text-sm text-muted-foreground">
          Semantic, symbol, and path search across your indexed projects.
        </p>
      </header>

      <Card>
        <CardContent className="space-y-4 p-5">
          <SearchInput
            value={draft}
            onChange={setDraft}
            onSubmit={commitQuery}
            placeholder={placeholderFor(mode)}
          />
          <Tabs value={mode} onValueChange={(v) => setMode(v as SearchMode)}>
            <TabsList className="w-full justify-start overflow-x-auto">
              {SEARCH_MODES.map((m) => (
                <TabsTrigger key={m.id} value={m.id}>
                  {m.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <ModeSpecificHelp mode={mode} />
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-[260px_minmax(0,1fr)]">
        <aside className="space-y-5">
          <ProjectPicker value={projectHash} onChange={setProject} />
          <ModeFilters mode={mode} params={params} setParams={setParams} />
        </aside>
        <ResultsArea mode={mode} projectHash={projectHash} query={queryParam} params={params} />
      </div>
    </div>
  );
}

function placeholderFor(mode: SearchMode): string {
  switch (mode) {
    case 'semantic':
      return 'e.g. "JWT validation middleware" or "retry with backoff"';
    case 'symbols':
      return 'Symbol name (substring)';
    case 'definitions':
    case 'references':
      return 'Exact symbol name';
    case 'files':
      return 'File path substring';
  }
}

function ModeFilters({
  mode,
  params,
  setParams,
}: {
  mode: SearchMode;
  params: URLSearchParams;
  setParams: ReturnType<typeof useSearchParams>[1];
}) {
  const limit = Number(params.get('limit') ?? defaultLimit(mode));
  const minScore = Number(params.get('min_score') ?? '0.4');
  const langs = params.get('langs') ?? '';

  function update(key: string, value: string | undefined) {
    const p = new URLSearchParams(params);
    if (value === undefined || value === '') p.delete(key);
    else p.set(key, value);
    setParams(p, { replace: true });
  }

  return (
    <div className="space-y-4 rounded-lg border p-4">
      <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Filters</h3>
      <LimitInput value={limit} onChange={(v) => update('limit', String(v))} />
      {mode === 'semantic' ? (
        <>
          <MinScoreSlider value={minScore} onChange={(v) => update('min_score', v.toFixed(2))} />
          <LanguagesInput value={langs} onChange={(v) => update('langs', v)} />
        </>
      ) : null}
    </div>
  );
}

function defaultLimit(mode: SearchMode): number {
  switch (mode) {
    case 'references':
      return 50;
    case 'symbols':
    case 'files':
      return 20;
    default:
      return 10;
  }
}

function ResultsArea({
  mode,
  projectHash,
  query,
  params,
}: {
  mode: SearchMode;
  projectHash: string | undefined;
  query: string;
  params: URLSearchParams;
}) {
  if (!projectHash) {
    return <PromptCard icon={FileQuestion} title="Choose a project to search in." />;
  }
  if (query.trim().length < 2) {
    return <PromptCard icon={SearchIcon} title="Type a query to begin." />;
  }
  switch (mode) {
    case 'semantic':
      return <SemanticResults projectHash={projectHash} query={query} params={params} />;
    case 'symbols':
      return <SymbolResults projectHash={projectHash} query={query} params={params} />;
    case 'definitions':
      return <DefinitionResults projectHash={projectHash} query={query} params={params} />;
    case 'references':
      return <ReferenceResults projectHash={projectHash} query={query} params={params} />;
    case 'files':
      return <FileResults projectHash={projectHash} query={query} params={params} />;
  }
}

type ResultProps = {
  projectHash: string;
  query: string;
  params: URLSearchParams;
};

function SemanticResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '10');
  const minScore = Number(params.get('min_score') ?? '0.4');
  const langs = (params.get('langs') ?? '').split(',').map((s) => s.trim()).filter(Boolean);
  const body = useMemo(
    () => ({
      query,
      limit,
      min_score: minScore,
      languages: langs.length > 0 ? langs : undefined,
    }),
    [query, limit, minScore, langs.join(',')],
  );
  const q = useSemanticSearch(projectHash, body);

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResultsCard />;

  return (
    <div className="space-y-3">
      <ResultsMeta total={q.data.total} timeMs={q.data.query_time_ms} />
      {q.data.results.map((g) => (
        <ResultFileCard key={g.file_path} group={g} />
      ))}
    </div>
  );
}

function SymbolResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '20');
  const body = useMemo(() => ({ query, limit }), [query, limit]);
  const q = useSymbolSearch(projectHash, body);

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResultsCard />;

  return (
    <div className="space-y-2">
      <ResultsMeta total={q.data.total} />
      <Card>
        <CardContent className="divide-y p-0">
          {q.data.results.map((s, i) => (
            <div key={`${s.file_path}:${s.name}:${i}`} className="flex items-center gap-3 px-4 py-2.5">
              <Badge variant="outline" className="shrink-0 text-[10px] uppercase">
                {s.kind}
              </Badge>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium font-mono">{s.name}</div>
                <div className="truncate text-xs text-muted-foreground" title={s.file_path}>
                  {s.file_path}:{s.line}
                </div>
              </div>
              <span className="shrink-0 text-xs text-muted-foreground">{s.language}</span>
              <OpenInEditorButton path={s.file_path} line={s.line} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}

function DefinitionResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '10');
  const body = useMemo(() => ({ symbol: query, limit }), [query, limit]);
  const q = useDefinitions(projectHash, body);

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResultsCard />;

  return (
    <div className="space-y-2">
      <ResultsMeta total={q.data.total} />
      <Card>
        <CardContent className="divide-y p-0">
          {q.data.results.map((d, i) => (
            <div key={`${d.file_path}:${d.line}:${i}`} className="flex items-center gap-3 px-4 py-2.5">
              <Badge variant="outline" className="shrink-0 text-[10px] uppercase">
                {d.kind}
              </Badge>
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-sm font-medium">{d.name}</div>
                <div className="truncate text-xs text-muted-foreground" title={d.file_path}>
                  {d.file_path}:{d.line}
                </div>
                {d.signature ? (
                  <div className="truncate font-mono text-xs text-muted-foreground" title={d.signature}>
                    {d.signature}
                  </div>
                ) : null}
              </div>
              <span className="shrink-0 text-xs text-muted-foreground">{d.language}</span>
              <OpenInEditorButton path={d.file_path} line={d.line} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}

function ReferenceResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '50');
  const body = useMemo(() => ({ symbol: query, limit }), [query, limit]);
  const q = useReferences(projectHash, body);

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResultsCard />;

  return (
    <div className="space-y-2">
      <ResultsMeta total={q.data.total} />
      <Card>
        <CardContent className="divide-y p-0">
          {q.data.results.map((r, i) => (
            <div
              key={`${r.file_path}:${r.start_line}:${i}`}
              className="flex items-center gap-3 px-4 py-2.5"
            >
              <span className="shrink-0 font-mono text-xs text-muted-foreground">
                L{r.start_line}
              </span>
              <code className="min-w-0 flex-1 truncate font-mono text-sm" title={r.file_path}>
                {r.file_path}
              </code>
              <span className="shrink-0 text-xs text-muted-foreground">{r.language}</span>
              <OpenInEditorButton path={r.file_path} line={r.start_line} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}

function FileResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '20');
  const body = useMemo(() => ({ query, limit }), [query, limit]);
  const q = useFileSearch(projectHash, body);

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResultsCard />;

  return (
    <div className="space-y-2">
      <ResultsMeta total={q.data.total} />
      <Card>
        <CardContent className="divide-y p-0">
          {q.data.results.map((f, i) => (
            <div key={`${f.file_path}:${i}`} className="flex items-center gap-3 px-4 py-2.5">
              <code className="min-w-0 flex-1 truncate font-mono text-sm" title={f.file_path}>
                {f.file_path}
              </code>
              {f.language ? (
                <span className="shrink-0 text-xs text-muted-foreground">{f.language}</span>
              ) : null}
              <OpenInEditorButton path={f.file_path} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}

function ResultsMeta({ total, timeMs }: { total: number; timeMs?: number }) {
  return (
    <div className="text-xs text-muted-foreground">
      {total} {total === 1 ? 'result' : 'results'}
      {typeof timeMs === 'number' ? ` · ${timeMs.toFixed(1)} ms` : ''}
    </div>
  );
}

function ResultsSkeleton() {
  return (
    <div className="space-y-3">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-20 w-full" />
      ))}
    </div>
  );
}

function ResultsError({ error }: { error: unknown }) {
  return (
    <Alert variant="destructive">
      <AlertCircle className="h-4 w-4" />
      <AlertTitle>Search failed</AlertTitle>
      <AlertDescription>
        {error instanceof ApiError ? error.detail : String(error)}
      </AlertDescription>
    </Alert>
  );
}

function NoResultsCard() {
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-2 py-12 text-center">
        <SearchIcon className="h-8 w-8 text-muted-foreground" />
        <p className="text-sm font-medium">No matches</p>
        <p className="max-w-sm text-xs text-muted-foreground">
          Try a different query, lower the minimum score, or pick a different mode.
        </p>
      </CardContent>
    </Card>
  );
}

function PromptCard({
  icon: Icon,
  title,
}: {
  icon: typeof SearchIcon;
  title: string;
}) {
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-2 py-16 text-center">
        <Icon className="h-8 w-8 text-muted-foreground" />
        <p className="text-sm">{title}</p>
      </CardContent>
    </Card>
  );
}
