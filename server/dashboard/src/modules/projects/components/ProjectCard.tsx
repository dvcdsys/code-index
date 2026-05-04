import { Link } from 'react-router-dom';
import { AlertTriangle, ChevronRight, Database, FileText } from 'lucide-react';
import type { Project } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import { formatRelative } from '@/lib/formatDate';
import { useRuntimeModel } from '@/lib/useServerStatus';

function basename(p: string): string {
  const parts = p.replace(/\/+$/, '').split('/');
  return parts[parts.length - 1] || p;
}

const STATUS_VARIANT: Record<Project['status'], 'default' | 'secondary' | 'destructive' | 'outline'> = {
  created: 'outline',
  indexing: 'secondary',
  indexed: 'default',
  error: 'destructive',
};

export function ProjectCard({ project }: { project: Project }) {
  const currentModel = useRuntimeModel();
  // Drift = the project was indexed under a different model than the one
  // the sidecar is running right now. NULL indexed_with_model is a legacy
  // row from before drift tracking landed — not drift, just unknown.
  const drift =
    !!project.indexed_with_model &&
    !!currentModel &&
    project.indexed_with_model !== currentModel;

  return (
    <Link to={`/projects/${project.path_hash}`} className="group">
      <Card
        className={`h-full transition-colors ${
          drift
            ? 'border-destructive/60 hover:border-destructive'
            : 'hover:border-foreground/30'
        }`}
      >
        <CardContent className="space-y-3 p-5">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <div className="truncate text-base font-medium leading-tight">
                {basename(project.host_path)}
              </div>
              <div className="mt-0.5 truncate text-xs text-muted-foreground" title={project.host_path}>
                {project.host_path}
              </div>
            </div>
            <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={STATUS_VARIANT[project.status]} className="capitalize">
              {project.status}
            </Badge>
            {drift ? (
              <Badge variant="destructive" className="gap-1">
                <AlertTriangle className="h-3 w-3" />
                Stale model
              </Badge>
            ) : null}
            {project.languages.slice(0, 4).map((l) => (
              <Badge key={l} variant="outline" className="font-normal text-xs">
                {l}
              </Badge>
            ))}
            {project.languages.length > 4 ? (
              <span className="text-xs text-muted-foreground">+{project.languages.length - 4}</span>
            ) : null}
          </div>
          <div className="flex items-center gap-4 text-xs text-muted-foreground">
            <span className="inline-flex items-center gap-1">
              <FileText className="h-3.5 w-3.5" />
              {project.stats.indexed_files.toLocaleString()} files
            </span>
            <span className="inline-flex items-center gap-1">
              <Database className="h-3.5 w-3.5" />
              {project.stats.total_symbols.toLocaleString()} symbols
            </span>
            <span className="ml-auto">
              {project.last_indexed_at
                ? `Indexed ${formatRelative(project.last_indexed_at)}`
                : 'Never indexed'}
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}
