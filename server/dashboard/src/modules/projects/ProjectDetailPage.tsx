import { AlertCircle, ArrowLeft, Search } from 'lucide-react';
import { Link, useParams } from 'react-router-dom';
import { ApiError } from '@/api/client';
import type { Project } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardContent } from '@/ui/card';
import { Skeleton } from '@/ui/skeleton';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { DeleteProjectDialog } from './components/DeleteProjectDialog';
import { useProject, useProjectSummary } from './hooks';

const STATUS_VARIANT: Record<Project['status'], 'default' | 'secondary' | 'destructive' | 'outline'> = {
  created: 'outline',
  indexing: 'secondary',
  indexed: 'default',
  error: 'destructive',
};

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const project = useProject(id);
  const summary = useProjectSummary(id);

  if (project.isLoading) return <DetailSkeleton />;
  if (project.error || !project.data) {
    return (
      <Alert variant="destructive">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>Project not found</AlertTitle>
        <AlertDescription>
          {project.error instanceof ApiError ? project.error.detail : 'Unknown error'}
        </AlertDescription>
        <div className="mt-3">
          <Button asChild variant="ghost" size="sm">
            <Link to="/projects">
              <ArrowLeft className="mr-1 h-4 w-4" />
              Back to projects
            </Link>
          </Button>
        </div>
      </Alert>
    );
  }

  const p = project.data;
  const s = summary.data;

  return (
    <div className="space-y-8">
      <div>
        <Button asChild variant="ghost" size="sm" className="-ml-3 text-muted-foreground">
          <Link to="/projects">
            <ArrowLeft className="mr-1 h-4 w-4" />
            All projects
          </Link>
        </Button>
      </div>

      <header className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={STATUS_VARIANT[p.status]} className="capitalize">
            {p.status}
          </Badge>
          {p.languages.slice(0, 6).map((l) => (
            <Badge key={l} variant="outline" className="font-normal">
              {l}
            </Badge>
          ))}
        </div>
        <h1 className="break-all font-mono text-2xl font-semibold leading-tight">
          {p.host_path}
        </h1>
        <div className="flex flex-wrap items-center gap-4 text-sm text-muted-foreground">
          <span>Hash: <code className="rounded bg-muted px-1 py-0.5 text-xs">{p.path_hash}</code></span>
          <span>Created {formatRelative(p.created_at)}</span>
          <span>
            {p.last_indexed_at
              ? `Indexed ${formatRelative(p.last_indexed_at)} (${formatDateTime(p.last_indexed_at)})`
              : 'Never indexed'}
          </span>
        </div>
        <div className="flex flex-wrap gap-2 pt-2">
          <Button asChild variant="outline" size="sm">
            <Link to={`/search?project=${p.path_hash}`}>
              <Search className="mr-1 h-4 w-4" />
              Search in this project
            </Link>
          </Button>
          {isAdmin ? (
            <DeleteProjectDialog hash={p.path_hash} hostPath={p.host_path} redirectAfter />
          ) : null}
        </div>
      </header>

      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Files indexed" value={p.stats.indexed_files} sub={`of ${p.stats.total_files} total`} />
        <StatCard label="Chunks" value={p.stats.total_chunks} />
        <StatCard label="Symbols" value={s?.total_symbols ?? p.stats.total_symbols} />
        <StatCard label="Languages" value={p.languages.length} />
      </section>

      <section className="grid gap-6 lg:grid-cols-2">
        <div>
          <h2 className="mb-3 text-sm font-medium text-muted-foreground">Top directories</h2>
          {summary.isLoading ? (
            <Skeleton className="h-48 w-full" />
          ) : !s || s.top_directories.length === 0 ? (
            <p className="text-sm text-muted-foreground">No directories yet.</p>
          ) : (
            <Card>
              <CardContent className="divide-y p-0">
                {s.top_directories.map((d) => (
                  <div key={d.path} className="flex items-center justify-between gap-2 px-4 py-2.5">
                    <code className="truncate font-mono text-xs" title={d.path}>{d.path}</code>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {d.file_count.toLocaleString()} {d.file_count === 1 ? 'file' : 'files'}
                    </span>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </div>

        <div>
          <h2 className="mb-3 text-sm font-medium text-muted-foreground">Recent symbols</h2>
          {summary.isLoading ? (
            <Skeleton className="h-48 w-full" />
          ) : !s || s.recent_symbols.length === 0 ? (
            <p className="text-sm text-muted-foreground">No symbols indexed yet.</p>
          ) : (
            <Card>
              <CardContent className="divide-y p-0">
                {s.recent_symbols.slice(0, 12).map((sym, i) => (
                  <div
                    key={`${sym.file_path}:${sym.name}:${i}`}
                    className="flex items-center gap-3 px-4 py-2.5"
                  >
                    <Badge variant="outline" className="shrink-0 text-[10px] uppercase">
                      {sym.kind}
                    </Badge>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-medium">{sym.name}</div>
                      <div className="truncate text-xs text-muted-foreground" title={sym.file_path}>
                        {sym.file_path}
                      </div>
                    </div>
                    <span className="shrink-0 text-xs text-muted-foreground">{sym.language}</span>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </div>
      </section>

      <Alert>
        <AlertTitle>Reindexing</AlertTitle>
        <AlertDescription>
          Indexing reads files from the local filesystem and is driven by the CLI. Run{' '}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">cix reindex</code> for a one-shot
          rescan, or keep <code className="rounded bg-muted px-1 py-0.5 text-xs">cix watch</code>{' '}
          running for automatic updates on file change.
        </AlertDescription>
      </Alert>
    </div>
  );
}

function StatCard({ label, value, sub }: { label: string; value: number; sub?: string }) {
  return (
    <Card>
      <CardContent className="space-y-1 p-4">
        <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
        <div className="text-2xl font-semibold tabular-nums">{value.toLocaleString()}</div>
        {sub ? <div className="text-xs text-muted-foreground">{sub}</div> : null}
      </CardContent>
    </Card>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <Skeleton className="h-6 w-32" />
      <Skeleton className="h-9 w-full max-w-2xl" />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-24" />
        ))}
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-48" />
        <Skeleton className="h-48" />
      </div>
    </div>
  );
}
