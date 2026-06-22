import { useMemo, useState } from 'react';
import { AlertCircle, ChevronDown, FolderPlus, LayoutGrid, List, Search } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/ui/dropdown-menu';
import { Input } from '@/ui/input';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import { Skeleton } from '@/ui/skeleton';
import { ApiError } from '@/api/client';
import { useAuth } from '@/auth/useAuth';
import { useRuntimeModel } from '@/lib/useServerStatus';
import { AddRepoDialog } from '@/modules/workspaces/components/AddRepoDialog';
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
  // Sorting is a table-only affordance, defaulting to most-recently-indexed.
  const [sort, setSort] = useState<ProjectSort>({ key: 'last_indexed', dir: 'desc' });

  // Sidecar embedding model — resolves the "Stale model" status the same way
  // the badges do, so the status filter options/predicate match the column.
  const currentModel = useRuntimeModel();

  function changeView(v: ProjectView) {
    setView(v);
    setProjectView(v);
  }

  function toggleStatus(s: string) {
    setStatuses((prev) => (prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s]));
  }

  const projects = data?.projects;
  // Filter options come from the full list so they don't vanish as the user
  // narrows the view with the other filters.
  const languages = useMemo(() => (projects ? collectLanguages(projects) : []), [projects]);
  const statusOptions = useMemo(
    () => (projects ? collectStatuses(projects, currentModel) : []),
    [projects, currentModel],
  );
  const filtered = useMemo(
    () =>
      projects
        ? filterProjects(projects, { search, type, statuses, currentModel, language })
        : [],
    [projects, search, type, statuses, currentModel, language],
  );
  const rows = useMemo(
    () => (view === 'table' ? sortProjects(filtered, sort) : filtered),
    [filtered, sort, view],
  );

  const filterActive =
    search.trim() !== '' || type !== 'all' || statuses.length > 0 || language !== 'all';

  return (
    <div className="space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? filterActive
                ? `Showing ${rows.length} of ${data.total} ${
                    data.total === 1 ? 'project' : 'projects'
                  }`
                : `${data.total} indexed ${data.total === 1 ? 'project' : 'projects'}`
              : ' '}
          </p>
        </div>
        {/* Add repo here clones + indexes a GitHub repository as a
            standalone project. The new project lives in /projects with
            no workspace attachment — link it into specific workspaces
            from the workspace detail page if you want.

            External (GitHub-cloned) projects are admin-administered — only
            admins can create them. Hide the trigger from regular users so
            the UI doesn't dangle a button that would 403 on submit. */}
        {isAdmin && <AddRepoDialog onAdded={() => void refetch()} />}
      </header>

      {/* Toolbar: name search + type/status/language filters + view toggle.
          All filters apply to both views; column sorting is handled inside
          the table. */}
      {data && data.projects.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-[14rem] flex-1">
            <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Filter by name…"
              className="pl-8"
            />
          </div>
          <Select value={type} onValueChange={(v) => setType(v as TypeFilter)}>
            <SelectTrigger className="w-[9.5rem]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              <SelectItem value="external">External</SelectItem>
              <SelectItem value="local">Local</SelectItem>
            </SelectContent>
          </Select>
          {statusOptions.length > 0 ? (
            // Inclusive multi-select: pick any of the status badges that appear
            // in the data; a project is kept only if it carries ALL the ticked
            // ones. Options are derived from the current projects, not the raw
            // server enum, so they mirror what the Status column shows.
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" className="w-[11rem] justify-between font-normal">
                  <span className="truncate">
                    {statuses.length === 0
                      ? 'All statuses'
                      : statuses.length === 1
                        ? statuses[0]
                        : `${statuses.length} statuses`}
                  </span>
                  <ChevronDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-[11rem]">
                <DropdownMenuLabel>Status</DropdownMenuLabel>
                {statusOptions.map((s) => (
                  <DropdownMenuCheckboxItem
                    key={s}
                    checked={statuses.includes(s)}
                    // Keep the menu open so several can be ticked in one go.
                    onSelect={(e) => e.preventDefault()}
                    onCheckedChange={() => toggleStatus(s)}
                  >
                    {s}
                  </DropdownMenuCheckboxItem>
                ))}
                {statuses.length > 0 ? (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      onSelect={() => setStatuses([])}
                      className="justify-center text-muted-foreground"
                    >
                      Clear
                    </DropdownMenuItem>
                  </>
                ) : null}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
          {languages.length > 0 ? (
            <Select value={language} onValueChange={setLanguage}>
              <SelectTrigger className="w-[10rem]">
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
          <div className="flex items-center rounded-md border p-0.5">
            <Button
              type="button"
              variant={view === 'grid' ? 'default' : 'ghost'}
              size="icon"
              className="h-8 w-8"
              aria-label="Tiles view"
              aria-pressed={view === 'grid'}
              onClick={() => changeView('grid')}
            >
              <LayoutGrid className="h-4 w-4" />
            </Button>
            <Button
              type="button"
              variant={view === 'table' ? 'default' : 'ghost'}
              size="icon"
              className="h-8 w-8"
              aria-label="Table view"
              aria-pressed={view === 'table'}
              onClick={() => changeView('table')}
            >
              <List className="h-4 w-4" />
            </Button>
          </div>
        </div>
      ) : null}

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
      ) : rows.length === 0 ? (
        <NoMatches />
      ) : view === 'table' ? (
        <ProjectsTable projects={rows} sort={sort} onSortChange={setSort} />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {rows.map((p) => (
            <ProjectCard key={p.path_hash} project={p} />
          ))}
        </div>
      )}
    </div>
  );
}

function NoMatches() {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-12 text-center">
      <Search className="h-8 w-8 text-muted-foreground" />
      <p className="text-sm text-muted-foreground">No projects match your filters.</p>
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
          Use <strong>Add repo</strong> above to clone + index a GitHub
          repository, or register a local project from the CLI with{' '}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            cix init &lt;path&gt;
          </code>
          .
        </p>
      </div>
    </div>
  );
}
