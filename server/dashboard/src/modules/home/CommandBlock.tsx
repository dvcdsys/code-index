import { Check, Copy } from 'lucide-react';
import { Button } from '@/ui/button';
import { useCopy } from '@/lib/useCopy';

// A read-only, wrapping mono command box with a one-click Copy button — same
// look as the API-key popup's connect-command block. `command` is the literal
// text copied to the clipboard; render it verbatim so what the user sees is
// exactly what they paste.
export function CommandBlock({ command }: { command: string }) {
  const { copied, copy } = useCopy();
  return (
    <div className="flex items-stretch gap-2">
      <pre className="flex-1 overflow-auto rounded-md border bg-muted p-2 font-mono text-xs whitespace-pre-wrap break-all max-h-40">
        {command}
      </pre>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        className="self-start"
        onClick={() => void copy(command)}
        aria-label="Copy command"
      >
        {copied ? (
          <>
            <Check className="mr-1 h-4 w-4" />
            Copied
          </>
        ) : (
          <>
            <Copy className="mr-1 h-4 w-4" />
            Copy
          </>
        )}
      </Button>
    </div>
  );
}
