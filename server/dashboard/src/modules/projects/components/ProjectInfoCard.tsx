import type { Project } from '@/api/types';
import { Card, CardContent, CardHeader, CardTitle } from '@/ui/card';
import { formatDateTime, formatRelative } from '@/lib/formatDate';

function formatBytes(bytes?: number | null): string {
  if (!bytes || bytes <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

// ProjectInfoCard surfaces metadata that would otherwise require SSH'ing
// onto the box: which embedding model produced the vectors, where the
// SQLite + chromem-go state for this project lives, on-disk sizes.
// Storage fields are nullable — embeddings-disabled servers don't have
// resolvable paths and we want to render gracefully rather than show "0 B".
export function ProjectInfoCard({ project }: { project: Project }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Storage &amp; index info</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-[12rem,1fr]">
          <Row label="Indexed with model">
            {project.indexed_with_model ? (
              <code className="break-all text-xs">{project.indexed_with_model}</code>
            ) : (
              <span className="text-muted-foreground">Unknown (indexed before drift tracking landed)</span>
            )}
          </Row>
          <Row label="Last indexed">
            {project.last_indexed_at ? (
              <>
                {formatRelative(project.last_indexed_at)}{' '}
                <span className="text-muted-foreground">({formatDateTime(project.last_indexed_at)})</span>
              </>
            ) : (
              <span className="text-muted-foreground">Never</span>
            )}
          </Row>
          <Row label="SQLite database">
            <div className="space-y-0.5">
              {project.sqlite_path ? (
                <code className="break-all text-xs">{project.sqlite_path}</code>
              ) : (
                <span className="text-muted-foreground">Not available</span>
              )}
              {project.sqlite_size_bytes ? (
                <div className="text-xs text-muted-foreground">{formatBytes(project.sqlite_size_bytes)}</div>
              ) : null}
            </div>
          </Row>
          <Row label="Vector store">
            <div className="space-y-0.5">
              {project.chroma_path ? (
                <code className="break-all text-xs">{project.chroma_path}</code>
              ) : (
                <span className="text-muted-foreground">Not available</span>
              )}
              {project.chroma_size_bytes ? (
                <div className="text-xs text-muted-foreground">{formatBytes(project.chroma_size_bytes)}</div>
              ) : null}
            </div>
          </Row>
          <Row label="Total chunks">
            <span className="tabular-nums">{project.stats.total_chunks.toLocaleString()}</span>
          </Row>
          <Row label="Total symbols">
            <span className="tabular-nums">{project.stats.total_symbols.toLocaleString()}</span>
          </Row>
        </dl>
      </CardContent>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-sm font-medium text-muted-foreground">{label}</dt>
      <dd className="text-sm">{children}</dd>
    </>
  );
}
