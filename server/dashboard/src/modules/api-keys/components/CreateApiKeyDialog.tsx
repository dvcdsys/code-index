import { useEffect, useRef, useState } from 'react';
import { Check, Copy, Loader2, KeyRound, AlertTriangle, Terminal } from 'lucide-react';
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
import { cixConnectCommand } from '@/lib/cixServer';
import { useCreateApiKey } from '../hooks';

// Last-resort copy when the async Clipboard API isn't available — happens
// on plain HTTP deploys (non-localhost) and inside some embedded webviews.
// document.execCommand('copy') is deprecated but universally implemented as
// of 2026; keeping it as a fallback turns "no way to copy" into "always works".
function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;
  // The reveal screen auto-selects the "Full key" input on mount. On WebKit a
  // lingering selection/focus — or an execCommand('copy') that silently fails —
  // can make the copy read THAT selection instead of our textarea, so the
  // "Connect the cix CLI" button ended up copying the bare key. Drop the old
  // focus/selection first so only our textarea can be the copy source.
  const active = document.activeElement as HTMLElement | null;
  if (active && typeof active.blur === 'function') active.blur();
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  // Position off-screen instead of opacity:0. WebKit refuses
  // execCommand('copy') on nodes it treats as invisible (opacity:0) — it copies
  // nothing, so the stale selection wins. An off-screen but rendered textarea
  // copies reliably.
  ta.style.position = 'fixed';
  ta.style.top = '-9999px';
  ta.style.left = '-9999px';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  // iOS / older Safari ignore textarea.select(); setSelectionRange establishes
  // the range execCommand('copy') actually reads.
  ta.setSelectionRange(0, text.length);
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
  const [copiedKey, setCopiedKey] = useState(false);
  const [copiedCmd, setCopiedCmd] = useState(false);
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
    setCopiedKey(false);
    setCopiedCmd(false);
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

  // copyText copies one string to the clipboard, surfacing a toast on failure.
  // navigator.clipboard requires a secure context (HTTPS or localhost). On
  // bare-IP / HTTP deploys it throws — fall back to document.execCommand
  // through a transient textarea so users still get one-click copy.
  async function copyText(text: string): Promise<boolean> {
    try {
      if (window.isSecureContext && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        if (!legacyCopy(text)) throw new Error('legacy copy failed');
      }
      return true;
    } catch {
      toast.error('Could not copy automatically.', {
        description: 'Select the text, ⌘A / Ctrl-A, then copy.',
      });
      return false;
    }
  }

  // One-paste command that registers THIS server (URL + the freshly minted key,
  // as a single `server.<alias>` entry) in the cix CLI config and makes it the
  // default. The dashboard is served same-origin from cix-server, so
  // window.location.origin is exactly the base URL the CLI must talk to. Shared
  // with the home-page onboarding card via cixConnectCommand so they never drift.
  const connectCmd = cixConnectCommand(
    window.location.origin,
    window.location.host,
    revealed ?? '<key>'
  );

  async function copyKey() {
    if (!revealed) return;
    if (await copyText(revealed)) {
      setCopiedKey(true);
      window.setTimeout(() => setCopiedKey(false), 2000);
    }
  }

  async function copyConnectCmd() {
    if (await copyText(connectCmd)) {
      setCopiedCmd(true);
      window.setTimeout(() => setCopiedCmd(false), 2000);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Accidental dismissal of the reveal screen (outside-click, Escape) is
        // blocked at the DialogContent layer below via preventDefault, so it
        // never reaches here. The only close paths that DO reach onOpenChange
        // while a key is revealed are explicit user intent — the top-right X
        // button (the "I've saved it" button sets state directly) — so honor
        // every close and always reset. (An earlier guard returned here when a
        // key was revealed, which also swallowed the X click: the dialog could
        // then only be dismissed by reloading the page.)
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
                <Button type="button" variant="secondary" onClick={copyKey}>
                  {copiedKey ? (
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
            <div className="space-y-2">
              <Label htmlFor="apikey-connect-cmd" className="flex items-center gap-1.5">
                <Terminal className="h-4 w-4" />
                Connect the cix CLI
              </Label>
              <div className="flex items-stretch gap-2">
                {/* Read-only, wrapping command box: one paste registers this
                    server (URL + key as a single entry) and makes it the
                    default, so `cix search …` works right after. */}
                <pre
                  id="apikey-connect-cmd"
                  className="flex-1 overflow-auto rounded-md border bg-muted p-2 font-mono text-xs whitespace-pre-wrap break-all max-h-32"
                >
                  {connectCmd}
                </pre>
                <Button
                  type="button"
                  variant="secondary"
                  className="self-start"
                  onClick={copyConnectCmd}
                >
                  {copiedCmd ? (
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
                Paste this in a terminal with the cix CLI installed. It saves the
                server to <code>~/.cix/config.yaml</code> as the default, then{' '}
                <code>cix search &quot;…&quot;</code> works.
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
