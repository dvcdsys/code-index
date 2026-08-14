import type { FileMatch, NestedHit } from '@/api/types';
import { Button } from '@/ui/button';
import { cn } from '@/lib/cn';
import { openInEditor } from '@/lib/editorPreference';

// Relevance is a number, so it renders as a mono badge — and its colour is a
// second reading of the same number, not the only one: green ≥ .8, warn < .6,
// ink in between. The aria-label spells it out for screen readers.
export function ScoreBadge({ score, className }: { score: number; className?: string }) {
  const tone = score >= 0.8 ? 'is-ok' : score < 0.6 ? 'is-warn' : 'is-ink';
  return (
    <span
      className={cn('cix-badge tabular-nums', tone, className)}
      aria-label={`relevance ${score.toFixed(2)}`}
    >
      {score.toFixed(2)}
    </span>
  );
}

export function ResultSnippet({ filePath, match }: { filePath: string; match: FileMatch }) {
  const lines = match.content.split('\n');
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2 font-mono text-[11.5px] text-muted">
        <span>
          :{match.start_line}
          {match.end_line !== match.start_line ? `-${match.end_line}` : ''}
        </span>
        {match.symbol_name ? <span className="text-ink">{match.symbol_name}</span> : null}
        <span className="cix-badge is-quiet">{match.chunk_type}</span>
        <span className="ml-auto flex items-center gap-2">
          <ScoreBadge score={match.score} />
          <OpenInEditorButton path={filePath} line={match.start_line} />
        </span>
      </div>

      <pre className="cix-well m-0">
        <code>
          {lines.map((line, i) => (
            <div key={i} className="flex">
              <span className="mr-3 inline-block w-9 shrink-0 select-none text-right text-[rgb(var(--c-line-quiet))]">
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
    <div className="border-l-[3px] border-line-quiet pl-3 font-mono text-[11.5px] text-muted">
      <div className="mb-1">also matches</div>
      <ul className="flex flex-col gap-0.5">
        {hits.map((h, i) => (
          <li key={i} className="flex flex-wrap items-center gap-2">
            <span>
              :{h.start_line}
              {h.end_line !== h.start_line ? `-${h.end_line}` : ''}
            </span>
            {h.symbol_name ? <span className="text-ink">{h.symbol_name}</span> : null}
            <span>({h.chunk_type})</span>
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
  return (
    <Button
      size="sm"
      variant="ghost"
      className={cn('h-6 px-2 font-mono text-[11px]', className)}
      onClick={() => openInEditor(path, line)}
      title="Open in editor (choose which one in Settings)"
    >
      open ↗
    </Button>
  );
}
