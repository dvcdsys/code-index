import { Link } from 'react-router-dom';
import { useServerStatus } from '@/lib/useServerStatus';
import { useAuth } from '@/auth/useAuth';
import { cn } from '@/lib/cn';

// Footer spans the full width below the sidebar + main pane. Reads
// from the shared /status query (polled every 30 s) — server version
// on the left, llama sidecar liveness dot on the right. The "llama"
// label links to /server (admin-only page); viewers see plain text
// since the route isn't mounted for them.
export function Footer() {
  const { data, isLoading } = useServerStatus();
  const { user } = useAuth();
  const version = data?.server_version ?? 'dev';
  const alive = data?.model_loaded === true;
  const isAdmin = user?.role === 'admin';

  const dotClass = isLoading
    ? 'bg-muted-foreground/40'
    : alive
      ? 'bg-emerald-500'
      : 'bg-red-500';
  const dotTitle = isLoading
    ? 'Checking sidecar status…'
    : alive
      ? 'Sidecar is alive'
      : 'Sidecar is not responding';

  const indicator = (
    <>
      <span className={cn('h-2 w-2 rounded-full', dotClass)} aria-hidden />
      <span>llama</span>
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
