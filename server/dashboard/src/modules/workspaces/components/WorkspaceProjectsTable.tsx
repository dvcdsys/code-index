import { useState } from 'react';
import { Link } from 'react-router-dom';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { Badge, Status } from '@/ui/badge';
import { Button, Dots } from '@/ui/button';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/ui/dialog';
import { Table, TBody, TD, TH, THead, TR } from '@/ui/table';
import { formatDateTime, formatRelative } from '@/lib/formatDate';
import { isExternal as isExternalProject } from '@/modules/projects/lib/projectList';
import type { ProjectStatus, WorkspaceProject } from '../types';
import { isInFlight } from '../types';

// The projects inside a workspace, as a table — the same shape Projects, API
// Keys, Users and Login security already use. It used to be a stack of cards
// with the facts crammed into one wrapped meta line, which meant this was the
// only list in the dashboard you could not scan down a column of: no aligned
// status, no aligned dates, no way to compare two repos without reading two
// paragraphs.
//
// Unlink is the only action here — reindex, webhook config and delete all live
// on the project's own detail page, and duplicating them would mean two places
// to keep in sync.
export function WorkspaceProjectsTable({
  workspaceID,
  projects,
  onUnlinked,
}: {
  workspaceID: string;
  projects: WorkspaceProject[];
  onUnlinked: () => void;
}) {
  return (
    <Table card>
      <THead>
        <TR>
          <TH>Project</TH>
          <TH>Status</TH>
          <TH>Kind</TH>
          <TH>Languages</TH>
          <TH>Linked</TH>
          <TH>Indexed</TH>
          <TH align="right" className="w-[104px]">
            Actions
          </TH>
        </TR>
      </THead>
      <TBody>
        {projects.map((wp) => (
          <ProjectRow
            key={wp.project.host_path}
            workspaceID={workspaceID}
            wp={wp}
            onUnlinked={onUnlinked}
          />
        ))}
      </TBody>
    </Table>
  );
}

function ProjectRow({
  workspaceID,
  wp,
  onUnlinked,
}: {
  workspaceID: string;
  wp: WorkspaceProject;
  onUnlinked: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const failed = wp.project.status === 'failed' || wp.project.status === 'error';
  const isExternal = isExternalProject(wp.project);
  const hash = wp.project.path_hash ?? '';
  const languages = wp.project.languages ?? [];

  async function unlink() {
    setBusy(true);
    try {
      await api.delete<void>(`/workspaces/${workspaceID}/projects/${hash}`);
      setConfirm(false);
      onUnlinked();
    } catch (err) {
      toast.error('Could not unlink the project', {
        description: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <TR>
      <TD className="max-w-[380px]">
        <Link
          to={`/projects/${hash}`}
          className="block truncate font-mono text-[13px] font-semibold hover:text-accent"
          title={wp.project.host_path}
        >
          {wp.project.host_path}
        </Link>
      </TD>
      <TD className={failed ? 'text-accent' : undefined}>
        <StatusChip status={wp.project.status} />
      </TD>
      <TD>
        <Badge variant="quiet">{isExternal ? 'external' : 'local'}</Badge>
      </TD>
      <TD mono className="whitespace-nowrap text-dim">
        {languages.length === 0 ? (
          <span className="text-faint">—</span>
        ) : (
          <span title={languages.join(', ')}>
            {languages.slice(0, 3).join(' ')}
            {languages.length > 3 ? ` +${languages.length - 3}` : ''}
          </span>
        )}
      </TD>
      <TD mono className="whitespace-nowrap" title={formatDateTime(wp.added_at)}>
        {formatRelative(wp.added_at)}
      </TD>
      <TD
        mono
        className="whitespace-nowrap"
        title={wp.project.last_indexed_at ? formatDateTime(wp.project.last_indexed_at) : undefined}
      >
        {wp.project.last_indexed_at ? (
          formatRelative(wp.project.last_indexed_at)
        ) : (
          <span className="text-faint">never</span>
        )}
      </TD>
      <TD align="right">
        <Button
          variant="ghost"
          size="sm"
          disabled={busy}
          title="Remove from this workspace — the project itself stays"
          onClick={() => setConfirm(true)}
        >
          Unlink
        </Button>

        <Dialog open={confirm} onOpenChange={(next) => (!busy ? setConfirm(next) : null)}>
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle>Remove from this workspace?</DialogTitle>
            </DialogHeader>
            <DialogBody>
              <DialogDescription>
                Only the link is removed.{' '}
                <span className="font-mono text-ink">{wp.project.host_path}</span> stays indexed and
                reachable from Projects.
              </DialogDescription>
            </DialogBody>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setConfirm(false)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="danger" onClick={unlink} disabled={busy}>
                {busy ? <Dots /> : null}
                Unlink
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </TD>
    </TR>
  );
}

function StatusChip({ status }: { status: ProjectStatus }) {
  if (status === 'indexed') return <Status tone="ok">indexed</Status>;
  if (status === 'error' || status === 'failed') return <Status tone="busy">{status}</Status>;
  if (isInFlight(status)) return <Status tone="warn">{status}</Status>;
  return <Status tone="idle">{status}</Status>;
}
