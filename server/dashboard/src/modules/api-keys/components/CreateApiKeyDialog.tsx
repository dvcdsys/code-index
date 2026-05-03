import { useEffect, useRef, useState } from 'react';
import { Check, Copy, Loader2, KeyRound, AlertTriangle } from 'lucide-react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Button } from '@/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Input } from '@/ui/input';
import { Label } from '@/ui/label';
import { useCreateApiKey } from '../hooks';

// Last-resort copy when the async Clipboard API isn't available — happens
// on plain HTTP deploys (non-localhost) and inside some embedded webviews.
// document.execCommand('copy') is deprecated but universally implemented as
// of 2026; keeping it as a fallback turns "no way to copy" into "always works".
function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}

// Two-stage dialog: collect a name, then reveal the freshly minted key once.
// Once revealed, the dialog refuses outside-click and Escape — accidental
// dismissal would lose the unrecoverable secret. Only the explicit "I've
// saved it" / X button can close it.
export function CreateApiKeyDialog() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [revealed, setRevealed] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const create = useCreateApiKey();

  // Auto-select the revealed key as soon as it appears so users can ⌘C
  // immediately if the Copy button doesn't work in their context.
  useEffect(() => {
    if (revealed && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [revealed]);

  function reset() {
    setName('');
    setRevealed(null);
    setCopied(false);
    create.reset();
  }

  async function onCreate() {
    const trimmed = name.trim();
    if (!trimmed) return;
    try {
      const out = await create.mutateAsync({ name: trimmed });
      setRevealed(out.full_key);
    } catch (err) {
      const detail = err instanceof ApiError ? err.detail : String(err);
      toast.error('Failed to create API key', { description: detail });
    }
  }

  async function copyToClipboard() {
    if (!revealed) return;
    // navigator.clipboard requires a secure context (HTTPS or localhost). On
    // bare-IP / HTTP deploys it throws — fall back to document.execCommand
    // through a transient textarea so users still get one-click copy.
    try {
      if (window.isSecureContext && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(revealed);
      } else {
        if (!legacyCopy(revealed)) throw new Error('legacy copy failed');
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error('Could not copy automatically.', {
        description: 'Click the field, ⌘A / Ctrl-A to select, then copy.',
      });
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Once a key is revealed, only the explicit "I've saved it" button
        // (or close-X) may dismiss the dialog. Outside-click and Escape are
        // blocked at the DialogContent layer below; we still gate state-resets
        // here so a sibling dismiss path can't wipe the key silently.
        if (!next && revealed) return;
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <KeyRound className="mr-1 h-4 w-4" />
          New key
        </Button>
      </DialogTrigger>
      <DialogContent
        className="sm:max-w-lg"
        // Block accidental dismissal of the reveal screen — the key is
        // unrecoverable after close. Click-X header still works.
        onPointerDownOutside={(e) => {
          if (revealed) e.preventDefault();
        }}
        onEscapeKeyDown={(e) => {
          if (revealed) e.preventDefault();
        }}
        onInteractOutside={(e) => {
          if (revealed) e.preventDefault();
        }}
      >
        {revealed ? (
          <>
            <DialogHeader>
              <DialogTitle>API key created</DialogTitle>
              <DialogDescription>
                Copy and store this key now. You will not be able to view it again.
              </DialogDescription>
            </DialogHeader>
            <Alert variant="destructive">
              <AlertTriangle className="h-4 w-4" />
              <AlertTitle>Save it before closing</AlertTitle>
              <AlertDescription>
                The full value is shown exactly once. We store only a SHA-256
                hash; if you lose it you must revoke and create a new key.
              </AlertDescription>
            </Alert>
            <div className="space-y-2">
              <Label htmlFor="apikey-revealed">Full key</Label>
              <div className="flex items-stretch gap-2">
                {/* readonly Input + select-all-on-focus is the most reliable
                    fallback when navigator.clipboard is unavailable (e.g. HTTP
                    deploys without a secure context). User can always ⌘A → ⌘C. */}
                <Input
                  id="apikey-revealed"
                  ref={inputRef}
                  readOnly
                  value={revealed}
                  className="flex-1 font-mono text-xs"
                  onFocus={(e) => e.currentTarget.select()}
                  onClick={(e) => e.currentTarget.select()}
                />
                <Button type="button" variant="secondary" onClick={copyToClipboard}>
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
              <p className="text-xs text-muted-foreground">
                Click the field to select all, then ⌘C / Ctrl-C if the Copy
                button is blocked by your browser.
              </p>
            </div>
            <DialogFooter>
              <Button
                onClick={() => {
                  // Same path as the X-button — explicit user intent acknowledged.
                  setOpen(false);
                  reset();
                }}
              >
                I&rsquo;ve saved it
              </Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create API key</DialogTitle>
              <DialogDescription>
                Generate a long-lived bearer token for CLI / SDK use. The full key
                is shown once and never stored in plaintext.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-2">
              <Label htmlFor="apikey-name">Name</Label>
              <Input
                id="apikey-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. ci-bot, laptop, jenkins"
                autoFocus
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    void onCreate();
                  }
                }}
              />
            </div>
            <DialogFooter>
              <Button
                variant="ghost"
                onClick={() => setOpen(false)}
                disabled={create.isPending}
              >
                Cancel
              </Button>
              <Button onClick={onCreate} disabled={create.isPending || !name.trim()}>
                {create.isPending ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
                Create key
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
