import { useState } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import type { FileGroupResult } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Card, CardContent } from '@/ui/card';
import { cn } from '@/lib/cn';
import { OpenInEditorButton, ResultSnippet } from './ResultSnippet';

export function ResultFileCard({
  group,
  defaultOpen = true,
}: {
  group: FileGroupResult;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const matchCount = group.matches.length;

  return (
    <Card>
      <CardContent className="p-0">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className={cn(
            'flex w-full items-center gap-2 px-4 py-3 text-left',
            'hover:bg-muted/40'
          )}
        >
          {open ? (
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <code className="min-w-0 flex-1 truncate font-mono text-sm" title={group.file_path}>
            {group.file_path}
          </code>
          {group.language ? (
            <Badge variant="outline" className="shrink-0 font-normal text-xs">
              {group.language}
            </Badge>
          ) : null}
          <Badge variant="secondary" className="shrink-0 tabular-nums">
            {group.best_score.toFixed(2)}
          </Badge>
          <span className="shrink-0 text-xs text-muted-foreground">
            {matchCount} {matchCount === 1 ? 'match' : 'matches'}
          </span>
          <OpenInEditorButton
            path={group.file_path}
            line={group.matches[0]?.start_line}
            className="shrink-0"
          />
        </button>
        {open ? (
          <div className="space-y-4 border-t bg-background p-4">
            {group.matches.map((m, i) => (
              <ResultSnippet key={`${m.start_line}-${i}`} filePath={group.file_path} match={m} />
            ))}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
