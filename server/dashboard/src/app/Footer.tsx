import { Link } from 'react-router-dom';
import { useServerStatus } from '@/lib/useServerStatus';
import { useAuth } from '@/auth/useAuth';
import { cn } from '@/lib/cn';

// Footer spans the full width below the sidebar + main pane. Reads
// from the shared /status query (polled every 30 s) — server version
// on the left, embedding-provider indicator on the right.
//
// The label is the active provider kind ("ollama" / "openai" /
// "voyage") and the dot logic depends on whether the provider
// manages an in-process child:
//   ollama (manages_process=true): green when /health alive, red
//     otherwise — real liveness signal.
//   openai / voyage (manages_process=false): permanently green.
//     We don't ping remote APIs on every footer poll; failures
//     surface at search/embed time with diagnostics.
//
// The provider name links to /server (admin-only page); viewers see
// plain text since the route isn't mounted for them.
export function Footer() {
  const { data, isLoading } = useServerStatus();
  const { user } = useAuth();
  const version = data?.server_version ?? 'dev';
  const providerKind = data?.embedding_provider ?? '';
  const managesProcess = data?.embedding_provider_manages_process === true;
  const alive = data?.model_loaded === true;
  const isAdmin = user?.role === 'admin';

  const dotClass = isLoading
    ? 'bg-muted-foreground/40'
    : managesProcess
      ? alive
        ? 'bg-emerald-500'
        : 'bg-red-500'
      : 'bg-emerald-500';
  const dotTitle = isLoading
    ? 'Checking embedding provider status…'
    : managesProcess
      ? alive
        ? 'Ollama sidecar is alive'
        : 'Ollama sidecar is not responding'
      : providerKind
        ? `${providerKind} backend (no managed process)`
        : 'Embedding backend';

  const label = providerKind || 'embeddings';

  const indicator = (
    <>
      <span className={cn('h-2 w-2 rounded-full', dotClass)} aria-hidden />
      <span>{label}</span>
    </>
  );

  return (
    <footer className="flex items-center justify-between border-t bg-muted/20 px-5 py-2 text-xs text-muted-foreground">
      <span className="flex items-center gap-3">
        <a
          href="https://github.com/dvcdsys/code-index"
          target="_blank"
          rel="noreferrer noopener"
          className="rounded-md px-1 py-0.5 hover:bg-accent/60 hover:text-foreground"
          title="Source on GitHub"
        >
          cix v{version}
        </a>
        <a
          href="/docs"
          target="_blank"
          rel="noreferrer"
          className="rounded-md px-1 py-0.5 hover:bg-accent/60 hover:text-foreground"
          title="OpenAPI / Swagger UI"
        >
          docs
        </a>
      </span>
      {isAdmin ? (
        <Link
          to="/server"
          title={dotTitle}
          className="flex items-center gap-2 rounded-md px-1 py-0.5 hover:bg-accent/60 hover:text-foreground"
        >
          {indicator}
        </Link>
      ) : (
        <span className="flex items-center gap-2" title={dotTitle}>
          {indicator}
        </span>
      )}
    </footer>
  );
}
