import type { Project } from '@/api/types';
import { Card, CardBody, CardHead } from '@/ui/card';
import { formatBytes } from '@/lib/formatBytes';
import { formatDateTime, formatRelative } from '@/lib/formatDate';

// Metadata that would otherwise need an SSH session: which embedding model
// produced the vectors, where this project's SQLite and chromem-go state
// lives, and how big both are. Storage fields are nullable — an
// embeddings-disabled server has no resolvable paths, and "—" beats "0 B".
//
// Paths are long, so this is a two-column grid rather than a right-aligned
// key/value block: the value column wraps instead of truncating.
export function ProjectInfoCard({ project }: { project: Project }) {
  return (
    <Card>
      <CardHead title="Storage & index" />
      <CardBody>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-[13rem_minmax(0,1fr)]">
          <Row label="Indexed with model">
            {project.indexed_with_model ? (
              <code className="break-all font-mono text-[12.5px]">{project.indexed_with_model}</code>
            ) : (
              <span className="text-faint">unknown — indexed before drift tracking</span>
            )}
          </Row>
          <Row label="Last indexed">
            {project.last_indexed_at ? (
              <>
                {formatRelative(project.last_indexed_at)}{' '}
                <span className="text-muted">({formatDateTime(project.last_indexed_at)})</span>
              </>
            ) : (
              <span className="text-faint">never</span>
            )}
          </Row>
          <Row label="SQLite database">
            {project.sqlite_path ? (
              <code className="break-all font-mono text-[12.5px]">{project.sqlite_path}</code>
            ) : (
              <span className="text-faint">not available</span>
            )}
            {project.sqlite_size_bytes ? (
              <div className="cix-hint mt-0.5">{formatBytes(project.sqlite_size_bytes)}</div>
            ) : null}
          </Row>
          <Row label="Vector store">
            {project.chroma_path ? (
              <code className="break-all font-mono text-[12.5px]">{project.chroma_path}</code>
            ) : (
              <span className="text-faint">not available</span>
            )}
            {project.chroma_size_bytes ? (
              <div className="cix-hint mt-0.5">{formatBytes(project.chroma_size_bytes)}</div>
            ) : null}
          </Row>
          <Row label="Total chunks">
            <span className="font-mono tabular-nums">
              {project.stats.total_chunks.toLocaleString()}
            </span>
          </Row>
          <Row label="Total symbols">
            <span className="font-mono tabular-nums">
              {project.stats.total_symbols.toLocaleString()}
            </span>
          </Row>
        </dl>
      </CardBody>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="cix-label pt-0.5">{label}</dt>
      <dd className="m-0 min-w-0 text-sm">{children}</dd>
    </>
  );
}
