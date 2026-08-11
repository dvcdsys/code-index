import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { Link } from 'react-router-dom';
import { useServerStatus } from '@/lib/useServerStatus';
import { useAuth } from '@/auth/useAuth';
import { formatVersion } from '@/lib/version';
import { cn } from '@/lib/cn';

// ---------------------------------------------------------------------------
// Page fact
//
// The status bar's middle slot belongs to whichever page is mounted: "7
// results · 128 ms" on search, "unsaved changes" on the server page, the host
// everywhere else. Pages publish theirs with useStatusFact(); it clears
// automatically on unmount, so a stale fact can't outlive its page.
// ---------------------------------------------------------------------------

const FactContext = createContext<{
  fact: ReactNode;
  setFact: (f: ReactNode) => void;
} | null>(null);

export function StatusFactProvider({ children }: { children: ReactNode }) {
  const [fact, setFact] = useState<ReactNode>(null);
  const value = useMemo(() => ({ fact, setFact }), [fact]);
  return <FactContext.Provider value={value}>{children}</FactContext.Provider>;
}

export function useStatusFact(fact: ReactNode): void {
  const ctx = useContext(FactContext);
  const setFact = ctx?.setFact;
  useEffect(() => {
    if (!setFact) return;
    setFact(fact);
    return () => setFact(null);
  }, [setFact, fact]);
}

// ---------------------------------------------------------------------------
// The bar itself
// ---------------------------------------------------------------------------

// Spans the full window width, under the sidebar — it is a sibling of the
// sidebar+main row, never a child of <main>. Left: build identity. Middle:
// the current page's fact. Right: the embedding provider.
//
// The dot logic depends on whether the provider manages an in-process child:
//   ollama (manages_process=true): green when /health is alive, red when not
//     — a real liveness signal.
//   openai / voyage (manages_process=false): green. We don't ping a paid
//     remote API every 30 s; failures surface at search/embed time.
export function StatusBar() {
  const { data, isLoading } = useServerStatus();
  const { user } = useAuth();
  const ctx = useContext(FactContext);

  const version = formatVersion(data?.server_version ?? 'dev');
  const providerKind = data?.embedding_provider || 'embeddings';
  const managesProcess = data?.embedding_provider_manages_process === true;
  const alive = data?.model_loaded === true;
  const isAdmin = user?.role === 'admin';

  const tone = isLoading ? 'idle' : managesProcess ? (alive ? 'ok' : 'down') : 'ok';
  const title = isLoading
    ? 'Checking the embedding provider…'
    : managesProcess
      ? alive
        ? 'Sidecar is alive'
        : 'Sidecar is not responding'
      : `${providerKind} backend (no managed process)`;

  const indicator = (
    <>
      <span
        aria-hidden
        className={cn(
          'cix-dot',
          tone === 'ok' && 'is-ok',
          tone === 'down' && 'is-busy',
          tone === 'idle' && ''
        )}
      />
      <span>{providerKind}</span>
    </>
  );

  return (
    <footer className="cix-statusbar">
      <a
        href="https://github.com/dvcdsys/code-index"
        target="_blank"
        rel="noreferrer noopener"
        title="Source on GitHub"
        className="text-ink hover:text-accent"
      >
        cix {version}
      </a>
      <a
        href="/docs"
        target="_blank"
        rel="noreferrer"
        title="OpenAPI / Swagger UI"
        className="text-accent hover:text-accent-deep"
      >
        docs
      </a>

      <span className="flex-1" />

      {ctx?.fact ? <span className="truncate">{ctx.fact}</span> : null}
      <span className="hidden sm:inline">{window.location.host}</span>

      {isAdmin ? (
        <Link to="/server" title={title} className="flex items-center gap-2 text-muted hover:text-ink">
          {indicator}
        </Link>
      ) : (
        <span className="flex items-center gap-2" title={title}>
          {indicator}
        </span>
      )}
    </footer>
  );
}
