import { useState } from 'react';
import type { FileGroupResult } from '@/api/types';
import { cn } from '@/lib/cn';
import { OpenInEditorButton, ResultSnippet, ScoreBadge } from './ResultSnippet';

// One card per file: `path :lines` on a header strip with the best score on
// the right, dark code excerpts underneath. Collapsing hides the excerpts but
// keeps the path and score — the row stays scannable either way.
//
// The header is a flex row, not one big <button>: the "open in editor" action
// lives in it, and a button inside a button is invalid HTML that also makes
// the open click toggle the card. The toggle is the path itself.
export function ResultFileCard({
  group,
  defaultOpen = true,
}: {
  group: FileGroupResult;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const count = group.matches.length;
  const firstLine = group.matches[0]?.start_line;
  const lastLine = group.matches[0]?.end_line;

  return (
    <div className="cix-card">
      <div
        className={cn(
          'flex items-center gap-2.5 px-[18px] py-3',
          open && 'border-b',
          'hover:bg-surface-hover'
        )}
      >
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          className="min-w-0 flex-1 truncate text-left font-mono text-[13.5px] font-bold"
          title={group.file_path}
        >
          <span aria-hidden className="mr-2 inline-block w-2 text-[10px] font-normal text-muted">
            {open ? '▾' : '▸'}
          </span>
          {group.file_path}
          {typeof firstLine === 'number' ? (
            <span className="ml-2 font-normal text-muted">
              :{firstLine}
              {lastLine && lastLine !== firstLine ? `-${lastLine}` : ''}
            </span>
          ) : null}
        </button>
        {group.language ? <span className="cix-badge is-quiet">{group.language}</span> : null}
        <span className="font-mono text-[11.5px] text-muted">
          {count} {count === 1 ? 'match' : 'matches'}
        </span>
        <ScoreBadge score={group.best_score} />
        <OpenInEditorButton path={group.file_path} line={firstLine} />
      </div>

      {open ? (
        <div className="flex flex-col gap-4 p-[18px]">
          {group.matches.map((m, i) => (
            <ResultSnippet key={`${m.start_line}-${i}`} filePath={group.file_path} match={m} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
