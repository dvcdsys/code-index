import { useProjects } from '@/modules/projects/hooks';
import { projectPath } from '@/modules/projects/lib/projectList';
import { Field, Input } from '@/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/ui/select';
import { SliderField } from '@/ui/slider';
import { cn } from '@/lib/cn';
import type { SearchMode } from '../hooks';

// The filter rail. A bordered column on the left of the results, not a
// floating card — the rule between rail and results is the page's spine.
export function FilterRail({ children }: { children: React.ReactNode }) {
  return (
    <aside className="flex flex-col gap-5 border-r p-[18px] lg:min-h-full">{children}</aside>
  );
}

export function ProjectPicker({
  value,
  onChange,
}: {
  value: string | undefined;
  onChange: (hash: string) => void;
}) {
  const { data, isLoading } = useProjects();
  const projects = data?.projects ?? [];

  return (
    <Field label="Project">
      <Select value={value ?? ''} onValueChange={onChange} disabled={isLoading}>
        <SelectTrigger aria-label="Project to search in">
          <SelectValue placeholder={isLoading ? 'Loading…' : 'Select a project'} />
        </SelectTrigger>
        <SelectContent>
          {projects.length === 0 ? (
            <div className="px-3 py-2 font-mono text-[11px] text-muted">No projects yet.</div>
          ) : (
            projects.map((p) => (
              <SelectItem key={p.path_hash} value={p.path_hash}>
                {projectPath(p)}
              </SelectItem>
            ))
          )}
        </SelectContent>
      </Select>
    </Field>
  );
}

export function MinScoreSlider({
  value,
  onChange,
}: {
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <SliderField
      label="Min score"
      value={value}
      display={value.toFixed(2)}
      onChange={onChange}
      min={0}
      max={1}
      step={0.05}
    />
  );
}

export function LimitInput({
  value,
  onChange,
  max = 100,
}: {
  value: number;
  onChange: (v: number) => void;
  max?: number;
}) {
  return (
    <Field label="Limit" htmlFor="limit">
      <Input
        id="limit"
        type="number"
        min={1}
        max={max}
        value={value}
        onChange={(e) => {
          const n = Number(e.target.value);
          if (Number.isFinite(n)) onChange(Math.max(1, Math.min(max, Math.round(n))));
        }}
      />
    </Field>
  );
}

// Languages as removable tags rather than a comma-string the user has to get
// right. The underlying URL param stays comma-separated so links keep working.
export function LanguageTags({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const tags = value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

  function add(raw: string) {
    const next = raw.trim().toLowerCase();
    if (!next || tags.includes(next)) return;
    onChange([...tags, next].join(','));
  }

  function remove(tag: string) {
    onChange(tags.filter((t) => t !== tag).join(','));
  }

  return (
    <Field label="Languages" hint={tags.length === 0 ? 'All languages.' : undefined}>
      <div className="flex flex-wrap items-center gap-1.5">
        {tags.map((t) => (
          <span key={t} className="cix-badge is-ink gap-1.5">
            {t}
            <button
              type="button"
              onClick={() => remove(t)}
              aria-label={`Remove ${t}`}
              className="text-surface/70 hover:text-surface"
            >
              ✕
            </button>
          </span>
        ))}
        <input
          type="text"
          placeholder="+ add"
          aria-label="Add a language filter"
          className={cn(
            'w-[72px] border border-dashed border-line-quiet bg-transparent px-2 py-[3px]',
            'font-mono text-[11px] text-ink placeholder:text-faint focus:border-solid focus:border-ink'
          )}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ',') {
              e.preventDefault();
              add(e.currentTarget.value);
              e.currentTarget.value = '';
            }
          }}
          onBlur={(e) => {
            add(e.currentTarget.value);
            e.currentTarget.value = '';
          }}
        />
      </div>
    </Field>
  );
}

export const MODE_HELP: Record<SearchMode, string> = {
  semantic: 'Natural language — "JWT validation", "retry with exponential backoff".',
  symbols: 'Substring match against symbol names.',
  definitions: 'Exact symbol name — where it is defined.',
  references: 'Exact symbol name — every call site.',
  files: 'Substring match against file paths.',
};
