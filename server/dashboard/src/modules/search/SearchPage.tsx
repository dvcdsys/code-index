import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError } from '@/api/client';
import { useStatusFact } from '@/app/StatusBar';
import { Callout } from '@/ui/alert';
import { Empty } from '@/ui/card';
import { Page } from '@/ui/page';
import { Skeleton } from '@/ui/skeleton';
import { SearchBar } from './components/SearchBar';
import {
  FilterRail,
  LanguageTags,
  LimitInput,
  MODE_HELP,
  MinScoreSlider,
  ProjectPicker,
} from './components/Filters';
import { ResultFileCard } from './components/ResultFileCard';
import { OpenInEditorButton, ScoreBadge } from './components/ResultSnippet';
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

// Layout: the query bar is the hero, modes are underline tabs directly under
// it, and the results sit in a two-column frame — bordered filter rail on the
// left, results on the right. All state lives in the URL so a search is a
// shareable link.
export default function SearchPage() {
  const [params, setParams] = useSearchParams();
  const mode = isMode(params.get('mode')) ? (params.get('mode') as SearchMode) : 'semantic';
  const projectHash = params.get('project') ?? undefined;
  const queryParam = params.get('q') ?? '';
  const [draft, setDraft] = useState(queryParam);

  // Typing changes `draft` and nothing else. The query in the URL — which is
  // what actually runs a search — moves only on submit.
  //
  // This used to debounce draft into the URL after 250ms idle. That is the
  // usual pattern and it is wrong here: a semantic search embeds the query
  // through the configured provider, so every pause while typing spent a real
  // API call and a full fan-out to answer a half-written question. "retry with
  // exponential backoff" typed at a normal pace fires on "retry", "retry with",
  // "retry with expo" — three searches nobody asked for and one they did.

  // Follow the URL when it changes from outside (a pasted link, back button).
  useEffect(() => {
    setDraft(queryParam);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryParam]);

  function update(key: string, value: string | undefined) {
    const p = new URLSearchParams(params);
    if (value === undefined || value === '') p.delete(key);
    else p.set(key, value);
    setParams(p, { replace: true });
  }

  const limit = Number(params.get('limit') ?? defaultLimit(mode));
  const minScore = Number(params.get('min_score') ?? '0.4');
  const langs = params.get('langs') ?? '';

  return (
    <Page title="Search" subtitle={`semantic · symbols · references across every indexed project`}>
      <SearchBar
        value={draft}
        onChange={setDraft}
        onSubmit={(v) => update('q', v.trim() || undefined)}
        placeholder={placeholderFor(mode)}
      />

      <div className="cix-tabs mt-5">
        {SEARCH_MODES.map((m) => (
          <button
            key={m.id}
            type="button"
            className={m.id === mode ? 'is-active' : undefined}
            onClick={() => update('mode', m.id)}
            title={m.description}
          >
            {m.label}
          </button>
        ))}
      </div>

      {/* overflow-hidden so the filter rail's square right border doesn't
          paint over the frame's rounded corners — same reason .cix-card
          carries it. */}
      <div className="mt-5 grid overflow-hidden rounded-card border lg:grid-cols-[264px_minmax(0,1fr)]">
        <FilterRail>
          <ProjectPicker value={projectHash} onChange={(h) => update('project', h)} />
          <LimitInput value={limit} onChange={(v) => update('limit', String(v))} />
          {mode === 'semantic' ? (
            <>
              <MinScoreSlider
                value={minScore}
                onChange={(v) => update('min_score', v.toFixed(2))}
              />
              <LanguageTags value={langs} onChange={(v) => update('langs', v)} />
            </>
          ) : null}
          <p className="mt-auto pt-2 font-mono text-[11px] leading-snug text-muted">
            {MODE_HELP[mode]}
          </p>
        </FilterRail>

        <div className="min-w-0 p-[18px]">
          <Results mode={mode} projectHash={projectHash} query={queryParam} params={params} />
        </div>
      </div>
    </Page>
  );
}

function placeholderFor(mode: SearchMode): string {
  switch (mode) {
    case 'semantic':
      return 'retry with exponential backoff';
    case 'symbols':
      return 'symbol name (substring)';
    case 'definitions':
    case 'references':
      return 'exact symbol name';
    case 'files':
      return 'file path substring';
  }
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

function Results({
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
    return (
      <Empty title="No project selected">
        Pick a project in the rail on the left — search is scoped to one indexed repository at a
        time.
      </Empty>
    );
  }
  if (query.trim().length < 2) {
    return (
      <Empty title="Type a query">
        At least two characters, then press Enter to search.
      </Empty>
    );
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

type ResultProps = { projectHash: string; query: string; params: URLSearchParams };

function SemanticResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '10');
  const minScore = Number(params.get('min_score') ?? '0.4');
  const langs = (params.get('langs') ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  const body = useMemo(
    () => ({
      query,
      limit,
      min_score: minScore,
      languages: langs.length > 0 ? langs : undefined,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [query, limit, minScore, langs.join(',')]
  );
  const q = useSemanticSearch(projectHash, body);

  useStatusFact(resultFact(q.data?.total, q.data?.query_time_ms));

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResults />;

  return (
    <>
      <ResultsMeta
        total={q.data.total}
        timeMs={q.data.query_time_ms}
        right={`score ≥ ${minScore.toFixed(2)}`}
      />
      <div className="flex flex-col gap-3">
        {q.data.results.map((g) => (
          <ResultFileCard key={g.file_path} group={g} />
        ))}
      </div>
    </>
  );
}

function SymbolResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '20');
  const body = useMemo(() => ({ query, limit }), [query, limit]);
  const q = useSymbolSearch(projectHash, body);

  useStatusFact(resultFact(q.data?.total));

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResults />;

  return (
    <>
      <ResultsMeta total={q.data.total} />
      <div className="cix-card">
        {q.data.results.map((s, i) => (
          <Row
            key={`${s.file_path}:${s.name}:${i}`}
            kind={s.kind}
            title={s.name}
            meta={`${s.file_path}:${s.line}`}
            language={s.language}
            path={s.file_path}
            line={s.line}
          />
        ))}
      </div>
    </>
  );
}

function DefinitionResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '10');
  const body = useMemo(() => ({ symbol: query, limit }), [query, limit]);
  const q = useDefinitions(projectHash, body);

  useStatusFact(resultFact(q.data?.total));

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResults />;

  return (
    <>
      <ResultsMeta total={q.data.total} />
      <div className="cix-card">
        {q.data.results.map((d, i) => (
          <Row
            key={`${d.file_path}:${d.line}:${i}`}
            kind={d.kind}
            title={d.name}
            meta={`${d.file_path}:${d.line}`}
            sub={d.signature ?? undefined}
            language={d.language}
            path={d.file_path}
            line={d.line}
          />
        ))}
      </div>
    </>
  );
}

function ReferenceResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '50');
  const body = useMemo(() => ({ symbol: query, limit }), [query, limit]);
  const q = useReferences(projectHash, body);

  useStatusFact(resultFact(q.data?.total));

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResults />;

  return (
    <>
      <ResultsMeta total={q.data.total} />
      <div className="cix-card">
        {q.data.results.map((r, i) => (
          <Row
            key={`${r.file_path}:${r.start_line}:${i}`}
            title={r.file_path}
            meta={`:${r.start_line}`}
            language={r.language}
            path={r.file_path}
            line={r.start_line}
            monoTitle
          />
        ))}
      </div>
    </>
  );
}

function FileResults({ projectHash, query, params }: ResultProps) {
  const limit = Number(params.get('limit') ?? '20');
  const body = useMemo(() => ({ query, limit }), [query, limit]);
  const q = useFileSearch(projectHash, body);

  useStatusFact(resultFact(q.data?.total));

  if (q.isLoading) return <ResultsSkeleton />;
  if (q.error) return <ResultsError error={q.error} />;
  if (!q.data || q.data.results.length === 0) return <NoResults />;

  return (
    <>
      <ResultsMeta total={q.data.total} />
      <div className="cix-card">
        {q.data.results.map((f, i) => (
          <Row
            key={`${f.file_path}:${i}`}
            title={f.file_path}
            language={f.language ?? undefined}
            path={f.file_path}
            monoTitle
          />
        ))}
      </div>
    </>
  );
}

// One list row: optional kind chip, title, mono meta line, language tag and
// the editor link. Shared by the four non-semantic modes so they can't drift.
function Row({
  kind,
  title,
  meta,
  sub,
  language,
  path,
  line,
  monoTitle,
}: {
  kind?: string;
  title: string;
  meta?: string;
  sub?: string;
  language?: string;
  path: string;
  line?: number;
  monoTitle?: boolean;
}) {
  return (
    <div className="cix-row">
      {kind ? <span className="cix-badge is-quiet uppercase">{kind}</span> : null}
      <div className="min-w-0 flex-1">
        <div
          className={`truncate ${monoTitle ? 'font-mono text-[13.5px]' : 'cix-row__title font-mono'}`}
          title={title}
        >
          {title}
        </div>
        {meta ? <div className="cix-row__meta truncate">{meta}</div> : null}
        {sub ? <div className="cix-row__meta truncate">{sub}</div> : null}
      </div>
      {language ? <span className="cix-row__meta flex-none">{language}</span> : null}
      <OpenInEditorButton path={path} line={line} />
    </div>
  );
}

function resultFact(total?: number, timeMs?: number): ReactNode {
  if (typeof total !== 'number') return null;
  return `${total} result${total === 1 ? '' : 's'}${
    typeof timeMs === 'number' ? ` · ${timeMs.toFixed(0)} ms` : ''
  }`;
}

function ResultsMeta({
  total,
  timeMs,
  right,
}: {
  total: number;
  timeMs?: number;
  right?: string;
}) {
  return (
    <div className="mb-3 flex items-baseline justify-between gap-3 font-mono text-[11.5px] text-muted">
      <span>
        <b className="text-ink">{total}</b> {total === 1 ? 'result' : 'results'}
        {typeof timeMs === 'number' ? ` · ${timeMs.toFixed(0)} ms` : ''}
      </span>
      {right ? <span>{right}</span> : null}
    </div>
  );
}

function ResultsSkeleton() {
  return (
    <div className="flex flex-col gap-3" aria-hidden>
      {Array.from({ length: 4 }, (_, i) => (
        <div key={i} className="cix-card p-[18px]">
          <Skeleton className="w-1/3" />
          <Skeleton className="mt-3 h-10 w-full" />
        </div>
      ))}
    </div>
  );
}

function ResultsError({ error }: { error: unknown }) {
  return (
    <Callout variant="danger">
      <b>Search failed</b>
      <p>{error instanceof ApiError ? error.detail : String(error)}</p>
    </Callout>
  );
}

function NoResults() {
  return (
    <Empty title="No matches">
      Try different wording, lower the minimum score, or switch modes — symbols and files match
      literally, semantic does not.
    </Empty>
  );
}

export { ScoreBadge };
