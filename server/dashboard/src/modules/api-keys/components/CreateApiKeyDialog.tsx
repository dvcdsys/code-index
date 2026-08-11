import { useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { ApiError } from '@/api/client';
import { Callout } from '@/ui/alert';
import { Button, Dots } from '@/ui/button';
import { CodeBlock } from '@/ui/code';
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/ui/dialog';
import { Field, Input } from '@/ui/input';
import { cixConnectCommand } from '@/lib/cixServer';
import { useCreateApiKey } from '../hooks';

// Last-resort copy when the async Clipboard API isn't available — happens on
// plain HTTP deploys (non-localhost) and inside some embedded webviews.
// document.execCommand('copy') is deprecated but universally implemented as of
// 2026; keeping it turns "no way to copy" into "always works".
function legacyCopy(text: string): boolean {
  // The reveal screen auto-selects the "Full key" input on mount. On WebKit a
  // lingering selection/focus can make the copy read THAT selection instead of
  // our textarea — the connect command once ended up copying the bare key.
  // Drop the old focus first so only our textarea can be the copy source.
  const active = document.activeElement as HTMLElement | null;
  if (active && typeof active.blur === 'function') active.blur();
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  // Off-screen rather than opacity:0 — WebKit refuses execCommand('copy') on
  // nodes it treats as invisible, and the stale selection wins instead.
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

// Two stages: name it, then reveal the key exactly once. On the reveal screen
// the dialog refuses outside-click and Escape — an accidental dismissal loses
// an unrecoverable secret. Only the X and the explicit acknowledge button close it.
export function CreateApiKeyDialog() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [revealed, setRevealed] = useState<string | null>(null);
  const [copiedKey, setCopiedKey] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const create = useCreateApiKey();

  // Auto-select the revealed key so ⌘C works immediately even if the Copy
  // button is blocked by the browser's clipboard policy.
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
    create.reset();
  }

  async function onCreate() {
    const trimmed = name.trim();
    if (!trimmed) return;
    try {
      const out = await create.mutateAsync({ name: trimmed });
      setRevealed(out.full_key);
    } catch (err) {
      toast.error('Could not create the API key', {
        description: err instanceof ApiError ? err.detail : String(err),
      });
    }
  }

  async function copyKey() {
    if (!revealed) return;
    let ok = false;
    try {
      if (window.isSecureContext && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(revealed);
        ok = true;
      } else {
        ok = legacyCopy(revealed);
      }
    } catch {
      ok = false;
    }
    if (!ok) {
      toast.error('Could not copy automatically.', {
        description: 'Click the field to select all, then ⌘C / Ctrl-C.',
      });
      return;
    }
    setCopiedKey(true);
    window.setTimeout(() => setCopiedKey(false), 2000);
  }

  // One paste registers THIS server (URL + the fresh key as a single
  // `server.<alias>` entry) in the CLI config and makes it the default. The
  // dashboard is served same-origin from cix-server, so window.location.origin
  // is exactly the base URL the CLI must talk to.
  const connectCmd = cixConnectCommand(
    window.location.origin,
    window.location.host,
    revealed ?? '<key>'
  );

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Accidental dismissal of the reveal screen is blocked below at the
        // DialogContent layer, so anything reaching here is explicit intent.
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary">New key</Button>
      </DialogTrigger>
      <DialogContent
        className="max-w-xl"
        onPointerDownOutside={(e) => revealed && e.preventDefault()}
        onEscapeKeyDown={(e) => revealed && e.preventDefault()}
        onInteractOutside={(e) => revealed && e.preventDefault()}
      >
        {revealed ? (
          <>
            <DialogHeader>
              <span className="cix-dot is-busy" aria-hidden />
              <DialogTitle>API key created</DialogTitle>
            </DialogHeader>
            <DialogBody>
              <Callout variant="danger">
                <b>Save it before closing</b>
                <p>
                  The full value is shown exactly once — only a SHA-256 hash is stored. If it is
                  lost, revoke this key and create another.
                </p>
              </Callout>

              <Field
                label="Full key"
                htmlFor="apikey-revealed"
                hint="Click the field to select all, then ⌘C / Ctrl-C if the button is blocked."
              >
                <div className="flex items-stretch gap-2">
                  <Input
                    id="apikey-revealed"
                    ref={inputRef}
                    readOnly
                    value={revealed}
                    className="flex-1"
                    onFocus={(e) => e.currentTarget.select()}
                    onClick={(e) => e.currentTarget.select()}
                  />
                  <Button onClick={copyKey}>{copiedKey ? 'Copied' : 'Copy'}</Button>
                </div>
              </Field>

              <Field
                label="Connect the cix CLI"
                hint="Saves this server to ~/.cix/config.yaml as the default, then `cix search` works."
              >
                <CodeBlock command={connectCmd} wrap />
              </Field>
            </DialogBody>
            <DialogFooter>
              <Button
                variant="primary"
                onClick={() => {
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
            </DialogHeader>
            <DialogBody>
              <DialogDescription>
                A long-lived bearer token for CLI and CI use. The full key is shown once and never
                stored in plaintext.
              </DialogDescription>
              <Field label="Name" htmlFor="apikey-name" hint="What will use this key.">
                <Input
                  id="apikey-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="ci-runner, laptop, jenkins"
                  autoFocus
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      void onCreate();
                    }
                  }}
                />
              </Field>
            </DialogBody>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setOpen(false)} disabled={create.isPending}>
                Cancel
              </Button>
              <Button variant="primary" onClick={onCreate} disabled={create.isPending || !name.trim()}>
                {create.isPending ? <Dots /> : null}
                Create key
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
