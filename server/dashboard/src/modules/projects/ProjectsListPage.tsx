import { AlertCircle, FolderPlus } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Skeleton } from '@/ui/skeleton';
import { ApiError } from '@/api/client';
import { ProjectCard } from './components/ProjectCard';
import { useProjects } from './hooks';

export function ProjectsListPage() {
  const { data, error, isLoading } = useProjects();

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
        <p className="text-sm text-muted-foreground">
          {data ? `${data.total} indexed ${data.total === 1 ? 'project' : 'projects'}` : ' '}
        </p>
      </header>

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-44 w-full" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Failed to load projects</AlertTitle>
          <AlertDescription>
            {error instanceof ApiError ? error.detail : String(error)}
          </AlertDescription>
        </Alert>
      ) : !data || data.projects.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.projects.map((p) => (
            <ProjectCard key={p.path_hash} project={p} />
          ))}
        </div>
      )}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <FolderPlus className="h-10 w-10 text-muted-foreground" />
      <div className="space-y-1">
        <p className="text-base font-medium">No projects yet</p>
        <p className="max-w-sm text-sm text-muted-foreground">
          Register a project from the CLI with{' '}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">cix init &lt;path&gt;</code>.
          A GitHub source will land here in a future PR.
        </p>
      </div>
    </div>
  );
}
