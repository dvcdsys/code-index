import { Loader2 } from 'lucide-react';
import { useProjects } from '@/modules/projects/hooks';
import { Label } from '@/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/ui/select';
import { Slider } from '@/ui/slider';
import { Input } from '@/ui/input';
import type { SearchMode } from '../hooks';

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
    <div className="space-y-1">
      <Label className="text-xs uppercase tracking-wide text-muted-foreground">Project</Label>
      <Select value={value ?? ''} onValueChange={onChange} disabled={isLoading}>
        <SelectTrigger className="h-9">
          {isLoading ? (
            <span className="inline-flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Loading…
            </span>
          ) : (
            <SelectValue placeholder="Select a project" />
          )}
        </SelectTrigger>
        <SelectContent>
          {projects.length === 0 ? (
            <div className="px-2 py-1.5 text-xs text-muted-foreground">No projects yet.</div>
          ) : (
            projects.map((p) => (
              <SelectItem key={p.path_hash} value={p.path_hash}>
                {p.host_path}
              </SelectItem>
            ))
          )}
        </SelectContent>
      </Select>
    </div>
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
    <div className="space-y-2">
      <div className="flex items-baseline justify-between">
        <Label className="text-xs uppercase tracking-wide text-muted-foreground">Min score</Label>
        <span className="font-mono text-xs tabular-nums">{value.toFixed(2)}</span>
      </div>
      <Slider
        value={[value]}
        min={0}
        max={1}
        step={0.05}
        onValueChange={([v]) => onChange(v)}
      />
    </div>
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
    <div className="space-y-1">
      <Label htmlFor="limit" className="text-xs uppercase tracking-wide text-muted-foreground">
        Limit
      </Label>
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
        className="h-9"
      />
    </div>
  );
}

export function LanguagesInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-1">
      <Label htmlFor="langs" className="text-xs uppercase tracking-wide text-muted-foreground">
        Languages
      </Label>
      <Input
        id="langs"
        placeholder="go, python, ts"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-9"
      />
      <p className="text-[10px] text-muted-foreground">Comma-separated. Leave empty for all.</p>
    </div>
  );
}

export function ModeSpecificHelp({ mode }: { mode: SearchMode }) {
  const messages: Record<SearchMode, string> = {
    semantic: 'Ask in natural language ("JWT validation", "retry with exponential backoff").',
    symbols: 'Substring match against symbol names.',
    definitions: 'Exact symbol name. Optional kind/file filters.',
    references: 'Exact symbol name. Returns every callsite.',
    files: 'Substring match against file paths.',
  };
  return <p className="text-xs text-muted-foreground">{messages[mode]}</p>;
}
