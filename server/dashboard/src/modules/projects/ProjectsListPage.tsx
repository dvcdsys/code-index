import { useMemo, useState } from 'react';
import { ApiError } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { useStatusFact } from '@/app/StatusBar';
import { useRuntimeModel } from '@/lib/useServerStatus';
import { AddRepoDialog } from '@/modules/workspaces/components/AddRepoDialog';
import { Callout } from '@/ui/alert';
import { Button } from '@/ui/button';
import { Empty } from '@/ui/card';
import { CheckboxRow } from '@/ui/checkbox';
import { Chip } from '@/ui/code';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/ui/dropdown-menu';
import { Input } from '@/ui/input';
import { Page } from '@/ui/page';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { Segmented } from '@/ui/tabs';
import { ProjectCard } from './components/ProjectCard';
import { ProjectsTable } from './components/ProjectsTable';
import { useProjects } from './hooks';
import {
  collectLanguages,
  collectStatuses,
  filterProjects,
  sortProjects,
  type ProjectSort,
  type TypeFilter,
} from './lib/projectList';
import { getProjectView, setProjectView, type ProjectView } from './lib/viewPreference';

export function ProjectsListPage() {
  const { data, error, isLoading, refetch } = useProjects();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';

  const [view, setView] = useState<ProjectView>(getProjectView);
  const [search, setSearch] = useState('');
  const [type, setType] = useState<TypeFilter>('all');
  const [statuses, setStatuses] = useState<string[]>([]);
  const [language, setLanguage] = useState('all');
  // Sorting is a table-only affordance; most-recently-indexed first.
  const [sort, setSort] = useState<ProjectSort>({ key: 'last_indexed', dir: 'desc' });

  // The sidecar's model resolves "Stale model" the same way the badges do, so
  // the filter options and the predicate always match the Status column.
  const currentModel = useRuntimeModel();

  function changeView(v: ProjectView) {
    setView(v);
    setProjectView(v);
  }

  function toggleStatus(s: string) {
    setStatuses((prev) => (prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s]));
  }

  const projects = data?.projects;
  // Options come from the full list so they don't vanish as the view narrows.
  const languages = useMemo(() => (projects ? collectLanguages(projects) : []), [projects]);
  const statusOptions = useMemo(
    () => (projects ? collectStatuses(projects, currentModel) : []),
    [projects, currentModel]
  );
  const filtered = useMemo(
    () =>
      projects ? filterProjects(projects, { search, type, statuses, currentModel, language }) : [],
    [projects, search, type, statuses, currentModel, language]
  );
  const rows = useMemo(
    () => (view === 'table' ? sortProjects(filtered, sort) : filtered),
    [filtered, sort, view]
  );

  const filterActive =
    search.trim() !== '' || type !== 'all' || statuses.length > 0 || language !== 'all';

  useStatusFact(
    data ? `${rows.length}${filterActive ? ` of ${data.total}` : ''} projects` : null
  );

  return (
    <Page
      title="Projects"
      subtitle="Indexed repositories on this server — local ones registered by the CLI, external ones cloned from GitHub."
      action={
        // External (GitHub-cloned) projects are admin-administered, so a
        // non-admin would only get a 403 on submit — no dangling button.
        isAdmin ? <AddRepoDialog onAdded={() => void refetch()} /> : null
      }
    >
      {data && data.projects.length > 0 ? (
        <div className="mb-5 flex flex-wrap items-center gap-2.5">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter by path…"
            aria-label="Filter projects by path"
            className="min-w-[14rem] flex-1"
          />

          <Select value={type} onValueChange={(v) => setType(v as TypeFilter)}>
            <SelectTrigger className="w-[150px]" aria-label="Project type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              <SelectItem value="external">External</SelectItem>
              <SelectItem value="local">Local</SelectItem>
            </SelectContent>
          </Select>

          {statusOptions.length > 0 ? (
            // Inclusive multi-select over the badges that actually appear in
            // the data: a project is kept only if it carries ALL the ticked
            // ones, so {Indexed, Out of sync} narrows rather than widens.
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button className="w-[176px] justify-between font-normal">
                  <span className="truncate">
                    {statuses.length === 0
                      ? 'All statuses'
                      : statuses.length === 1
                        ? statuses[0]
                        : `${statuses.length} statuses`}
                  </span>
                  <span aria-hidden className="font-mono text-[10px]">
                    ▼
                  </span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-[220px]">
                {statusOptions.map((s) => (
                  <div key={s} className="px-3 py-2">
                    <CheckboxRow
                      id={`status-${s}`}
                      checked={statuses.includes(s)}
                      onCheckedChange={() => toggleStatus(s)}
                      label={s}
                    />
                  </div>
                ))}
                {statuses.length > 0 ? (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onSelect={() => setStatuses([])}>Clear</DropdownMenuItem>
                  </>
                ) : null}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}

          {languages.length > 0 ? (
            <Select value={language} onValueChange={setLanguage}>
              <SelectTrigger className="w-[160px]" aria-label="Language">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All languages</SelectItem>
                {languages.map((l) => (
                  <SelectItem key={l} value={l}>
                    {l}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null}

          <Segmented
            aria-label="View"
            value={view}
            onChange={changeView}
            options={[
              { value: 'table', label: 'Table' },
              { value: 'grid', label: 'Tiles' },
            ]}
          />
        </div>
      ) : null}

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-44" />
          ))}
        </div>
      ) : error ? (
        <Callout variant="danger">
          <b>Could not load projects</b>
          <p>{error instanceof ApiError ? error.detail : String(error)}</p>
        </Callout>
      ) : !data || data.projects.length === 0 ? (
        <Empty title="No projects yet">
          Use <b>Add repo</b> above to clone and index a GitHub repository, or register a local one
          from the CLI with <Chip>cix init &lt;path&gt;</Chip>.
        </Empty>
      ) : rows.length === 0 ? (
        <Empty title="No matches">Nothing here fits the current filters.</Empty>
      ) : view === 'table' ? (
        <ProjectsTable projects={rows} sort={sort} onSortChange={setSort} />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {rows.map((p) => (
            <ProjectCard key={p.path_hash} project={p} />
          ))}
        </div>
      )}
    </Page>
  );
}
