import { Link, useNavigate } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { useServerStatus } from '@/lib/useServerStatus';
import { formatVersion } from '@/lib/version';
import { Page, SectionLabel } from '@/ui/page';
import { Button } from '@/ui/button';
import { Card, StatStrip } from '@/ui/card';
import { Callout } from '@/ui/alert';
import { visibleModules } from '../registry';
import { useProjects } from '../projects/hooks';
import { isDrifted, projectLabel } from '../projects/lib/projectList';
import { ConnectClaudeCode } from './ConnectClaudeCode';

// Home answers three questions in order: is the server healthy, how do I
// wire my editor to it, and where is everything else.
//
// The four server facts are one divided strip — not a floating card — so the
// page opens on hard values rather than on decoration.
export default function HomePage() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const { data: status } = useServerStatus();
  const { data: projectList } = useProjects();

  const modules = visibleModules(user?.role).filter((m) => m.id !== 'home');
  const runtimeModel = status?.embedding_model ?? '';
  const projects = projectList?.projects ?? [];

  // A project whose vectors were written by a different embedding model
  // returns nonsense for semantic queries until it is reindexed — that is the
  // one alert worth interrupting the landing page for.
  const stale = projects.filter((p) => isDrifted(p, runtimeModel || null));

  const indexed = projectList?.total ?? projects.length;

  return (
    <Page
      title={`Welcome back${user?.email ? `, ${user.email.split('@')[0]}` : ''}`}
      subtitle="Semantic code search, project management and runtime control for this cix server."
      action={
        <Button variant="primary" onClick={() => navigate('/projects')}>
          Index a project
        </Button>
      }
    >
      <StatStrip
        items={[
          { label: 'Server', value: formatVersion(status?.server_version ?? 'dev') },
          {
            label: 'Provider',
            value: (
              <span className="flex items-center gap-2">
                <span
                  className={`cix-dot ${status?.model_loaded ? 'is-ok' : 'is-warn'}`}
                  aria-hidden
                />
                <span className={status?.model_loaded ? 'text-ok' : 'text-warn-ink'}>
                  {status?.model_loaded ? 'ready' : 'loading'}
                </span>
              </span>
            ),
          },
          {
            label: 'Model',
            value: <span className="text-[15px]">{runtimeModel || '—'}</span>,
            title: runtimeModel,
          },
          {
            label: 'Indexed',
            value: (
              <span className="text-[17px]">
                {indexed} project{indexed === 1 ? '' : 's'}
              </span>
            ),
          },
        ]}
        className="mb-6"
      />

      {/* One column. The page is a sequence — check the server, wire the
          editor, then go somewhere — and a side rail turned that sequence
          into two things to read at once, with the setup steps squeezed into
          a narrow measure for no gain. Only the module tiles fan out. */}
      {/* min-w-0 on the children is load-bearing: a flex item defaults to
          min-width:auto, so the long install command inside ConnectClaudeCode
          would size the column to itself and push the whole page into a
          horizontal scroll. The old two-column grid hid this behind an
          explicit minmax(0,1fr). */}
      <div className="flex flex-col gap-6">
        <div className="min-w-0">
          <ConnectClaudeCode />
        </div>

        <div className="min-w-0">
          <SectionLabel>Modules</SectionLabel>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {modules.map((m) => (
              <Link key={m.id} to={m.path} className="min-w-0 focus-visible:outline-offset-4">
                <Card clickable className="h-full">
                  <div className="p-3.5">
                    <span className="block text-[14.5px] font-bold">{m.label}</span>
                    <span className="mt-1 block font-mono text-[11.5px] leading-snug text-muted">
                      {m.blurb}
                    </span>
                  </div>
                </Card>
              </Link>
            ))}
          </div>

          {stale.length > 0 && (
            <Callout variant="warn" className="mt-3">
              <b>
                {stale.length} project{stale.length === 1 ? '' : 's'} on a stale model
              </b>
              <p>
                {stale
                  .slice(0, 3)
                  .map(projectLabel)
                  .join(', ')}
                {stale.length > 3 ? ` +${stale.length - 3} more` : ''} — indexed with a different
                embedding model. A full reindex is needed before semantic search is trustworthy.
              </p>
              <Link to="/projects" className="cix-link-btn mt-1 self-start">
                Open Projects
              </Link>
            </Callout>
          )}
        </div>
      </div>
    </Page>
  );
}
