import { ExternalLink } from 'lucide-react';
import type { FileMatch, NestedHit } from '@/api/types';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { cn } from '@/lib/cn';

export function ResultSnippet({
  filePath,
  match,
}: {
  filePath: string;
  match: FileMatch;
}) {
  const lines = match.content.split('\n');
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span className="font-mono">
          L{match.start_line}
          {match.end_line !== match.start_line ? `–${match.end_line}` : ''}
        </span>
        {match.symbol_name ? (
          <span className="font-mono text-foreground">{match.symbol_name}</span>
        ) : null}
        <Badge variant="outline" className="font-normal">
          {match.chunk_type}
        </Badge>
        <Badge variant="secondary" className="ml-auto font-mono tabular-nums">
          {match.score.toFixed(2)}
        </Badge>
        <OpenInEditorButton path={filePath} line={match.start_line} />
      </div>
      <pre className="overflow-x-auto rounded-md border bg-muted/30 p-3 text-xs leading-relaxed">
        <code>
          {lines.map((line, i) => (
            <div key={i} className="flex">
              <span className="mr-3 inline-block w-8 shrink-0 select-none text-right text-muted-foreground/60">
                {match.start_line + i}
              </span>
              <span className="flex-1 whitespace-pre">{line || ' '}</span>
            </div>
          ))}
        </code>
      </pre>
      {match.nested_hits && match.nested_hits.length > 0 ? (
        <NestedHitsList hits={match.nested_hits} />
      ) : null}
    </div>
  );
}

function NestedHitsList({ hits }: { hits: NestedHit[] }) {
  return (
    <div className="ml-3 border-l border-muted pl-3 text-xs text-muted-foreground">
      <div className="mb-1 font-medium">Also matches:</div>
      <ul className="space-y-0.5">
        {hits.map((h, i) => (
          <li key={i} className="flex flex-wrap items-center gap-2">
            <span className="font-mono">
              L{h.start_line}
              {h.end_line !== h.start_line ? `–${h.end_line}` : ''}
            </span>
            {h.symbol_name ? <span className="font-mono">{h.symbol_name}</span> : null}
            <span className="opacity-70">({h.chunk_type})</span>
            <span className="ml-auto tabular-nums">{h.score.toFixed(2)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function OpenInEditorButton({
  path,
  line,
  className,
}: {
  path: string;
  line?: number;
  className?: string;
}) {
  function open() {
    // Try Cursor first, then VS Code. Browsers will silently no-op if no
    // handler is registered. A user-configurable preference moves to PR-D.
    const lineSuffix = line ? `:${line}` : '';
    const cursor = `cursor://file/${path}${lineSuffix}`;
    const vscode = `vscode://file/${path}${lineSuffix}`;
    window.location.href = cursor;
    setTimeout(() => {
      window.location.href = vscode;
    }, 250);
  }

  return (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      className={cn('h-6 px-2 text-xs', className)}
      onClick={open}
      title="Open in editor (Cursor → VS Code)"
    >
      <ExternalLink className="mr-1 h-3 w-3" />
      Open
    </Button>
  );
}
