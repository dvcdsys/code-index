import { useState, useEffect, useRef } from 'react';
import { DemoControls } from '../shared/demo-controls.jsx';
import { useFrameTimeouts } from '../shared/use-frame-timeouts.js';
import { SERVER_VERSION, MAC_APP_VERSION } from '../shared/versions.js';

// A scaled-down cix.app menu bar panel, playing back a first launch.
//
// This is a REPLICA, not a screenshot: the markup, the tokens and the
// state-by-state layout are transcribed from cli/launcher/panel.html — same
// cream surface, same 1.5px ink border, same blocky progress cells (never a
// rounded spinner), same in-panel dialog over a scrim instead of an osascript
// window. The wizard's and the confirmation's wording come from
// cli/launcher/firstrun_darwin.go and menu_darwin.go verbatim.
//
// The story is the point of the section: someone who has never run cix should
// be able to watch this once and know what installing it involves. So it runs
// the sequence end to end — the setup prompt → typing an address →
// "Downloading the cix server…" → the generated credentials → STARTING (a cold
// start loads the embedding model and really can take minutes, which is why it
// is its own state) → RUNNING → INDEXING → and then one setting changed, in the
// order the app does it: click, confirm, restart, new state.
//
// That last part is Allow Network Access rather than Launch at Login because it
// is the one whose effect is visible in the panel: the caption goes from
// "localhost only" to "reachable on your network" and the hint line names the
// address other machines use. CIX_BIND_ADDR is read once at process start, so
// the setting means nothing until the server has been restarted — the switch
// therefore moves when the server comes back, not when the click lands.
//
// Two deliberate deviations, both to keep the miniature honest at this size:
// the credentials card drops the password line from the message body (the app
// prints it there as well as in the copyable block, which at 320px reads as a
// rendering bug), and the values are stand-ins — versions.js for the versions,
// an example.com address, an obviously fake password of the right shape (24
// base64url characters, what generatePassword produces) and an RFC1918 address
// for the LAN line.

const MODEL = 'awhiteside/CodeRankEmbed-Q8_0-GGUF';
const PORT = '21847';
const EMAIL = 'you@example.com';
const PASSWORD = 'qX7mR2vK9pLtA4wZ8bN6cE1s';
const LAN_ADDR = `192.168.1.24:${PORT}`;

// firstrun_darwin.go's `intro`, unchanged.
const SETUP_INTRO = 'Enter an email address for the administrator account.\n\n'
  + 'It is the login for the cix dashboard on this Mac — nothing is sent anywhere. '
  + 'A password is generated for you, and setup then downloads the server (about 40 MB).';

// menu_darwin.go's toggleNetworkAccess confirmation, unchanged.
const NETWORK_ASK = `cix will accept connections from any machine that can reach this Mac on port ${PORT}, `
  + 'instead of only from this Mac.\n\n'
  + 'Accounts and API keys still apply — this does not disable authentication — but the login page '
  + 'and the API become reachable from your network.\n\n'
  + 'The server will restart.';

// phase → how long it holds. `typing` has no duration: it ends when the address
// is typed.
const SCRIPT = [
  { phase: 'ask', ms: 2000 },
  { phase: 'typing', ms: null },
  { phase: 'ok', ms: 600 },
  { phase: 'download', ms: 3200 },
  { phase: 'creds', ms: 5600 },
  { phase: 'starting', ms: 3200 },
  { phase: 'running', ms: 2600 },
  { phase: 'indexing', ms: 3400 },
  { phase: 'netclick', ms: 800 },
  { phase: 'netask', ms: 5600 },
  { phase: 'netallow', ms: 600 },
  { phase: 'restart', ms: 2800 },
  { phase: 'netup', ms: 5000 },
];
const LAST = SCRIPT.length - 1;

// The first-run wizard's phases, and the ones the toggle holds `busy` for —
// beginBusy("Applying the setting…") covers the confirmation and the restart,
// which is why the action button is a loader for all of them.
const SETUP_PHASES = ['ask', 'typing', 'ok', 'download', 'creds'];
const BUSY_PHASES = ['netclick', 'netask', 'netallow', 'restart'];

const SWEEP_CELLS = 8;
const SWEEP_LIT = 3;
const SWEEP_MS = 220;
const TYPE_MS = 55;

export function MacPanel() {
  const [idx, setIdx] = useState(0);
  const [running, setRunning] = useState(true);
  const [started, setStarted] = useState(false);
  const [sweep, setSweep] = useState(0);
  const [typed, setTyped] = useState('');
  const rootRef = useRef(null);

  // TWO schedulers, not one. clearAll() is per-instance and wipes every pending
  // callback in it — so a sweep that re-arms every 220 ms sharing a queue with
  // the phase timer means its cleanup deletes the pending phase change, and the
  // demo stops dead on the first sweeping state. That is exactly what happened:
  // it sat on STARTING forever. The sweep and the typing never overlap
  // (different phases), so those two can share the second instance.
  const phaseClock = useFrameTimeouts();
  const anim = useFrameTimeouts();

  const phase = SCRIPT[idx].phase;
  const sweeping = phase === 'starting' || phase === 'indexing' || phase === 'restart';

  // Hold until the panel is on screen — an animation nobody has scrolled to is
  // only a battery cost.
  useEffect(() => {
    const el = rootRef.current;
    if (!el || started) return undefined;
    if (typeof IntersectionObserver === 'undefined') { setStarted(true); return undefined; }
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting) { setStarted(true); io.disconnect(); }
    }, { threshold: 0.3 });
    io.observe(el);
    return () => io.disconnect();
  }, [started]);

  useEffect(() => {
    if (!running || !started) return phaseClock.clearAll;
    const { ms } = SCRIPT[idx];
    if (ms == null) return phaseClock.clearAll; // self-advancing phase
    phaseClock.T(() => setIdx(i => (i + 1) % SCRIPT.length), ms);
    return phaseClock.clearAll;
  }, [running, started, idx]);

  useEffect(() => {
    if (!running || !started || phase !== 'typing') return undefined;
    if (typed.length < EMAIL.length) {
      anim.T(() => setTyped(EMAIL.slice(0, typed.length + 1)), TYPE_MS);
    } else {
      anim.T(() => setIdx(i => i + 1), 450);
    }
    return anim.clearAll;
  }, [running, started, phase, typed]);

  useEffect(() => {
    if (!sweeping || !running || !started) return undefined;
    anim.T(() => setSweep(s => (s + 1) % SWEEP_CELLS), SWEEP_MS);
    return anim.clearAll;
  }, [sweeping, running, started, sweep]);

  // The address is cleared on the way back round so the loop starts on an empty
  // field, not on last cycle's answer.
  useEffect(() => { if (phase === 'ask') setTyped(''); }, [phase]);

  const setup = SETUP_PHASES.includes(phase);
  const busy = BUSY_PHASES.includes(phase);
  const isRunning = ['running', 'indexing', 'netclick', 'netask', 'netallow', 'netup'].includes(phase);
  const word = phase === 'indexing' ? 'INDEXING'
    : isRunning ? 'RUNNING'
    : phase === 'starting' || phase === 'restart' ? 'STARTING' : 'STOPPED';
  const tone = word === 'RUNNING' ? 'ok' : word === 'STOPPED' ? 'idle' : 'accent';

  // The bind address takes effect at process start, so the switch moves when
  // the restarted server reports the new binding — not on the click.
  const networked = phase === 'netup';

  // Before the runtime is downloaded there is nothing installed to report, so
  // the stopped table is the app's own version and nothing else.
  const rows = isRunning || phase === 'restart'
    ? [['process', '78903'], ['engine', 'llama.cpp (bundled)'], ['model', MODEL],
       ['server', SERVER_VERSION], ['app', MAC_APP_VERSION]]
    : setup
      ? [['app', MAC_APP_VERSION]]
      : [['installed', SERVER_VERSION], ['app', MAC_APP_VERSION]];

  const caption = {
    ask: 'the setup question', typing: 'the setup question', ok: 'the setup question',
    download: 'fetching the server', creds: 'your login, once',
    starting: 'cold start', running: 'up', indexing: 'working',
    netclick: 'changing a setting', netask: 'changing a setting',
    netallow: 'changing a setting', restart: 'restarting to apply it',
    netup: 'now reachable from your network',
  }[phase];

  return (
    <div className="mac-stage" ref={rootRef}>
      <span className="mac-stage-label">cix.app · first launch · {caption}</span>

      {/* The slot reserves the tallest state's height so the panel can keep
          resizing with its content — which is what the real one does — without
          moving anything else on the page. */}
      <div className="mac-panel-slot">
        <div
          className={`mac-panel ${phase} ${setup || phase === 'netask' || phase === 'netallow' ? 'dlg' : ''}`}
          role="img"
          aria-label={`cix.app menu bar panel, ${setup ? 'first-run setup' : word.toLowerCase()}`}
        >
          <header className="mp-hd">
            <div className="mp-statusline">
              <span className={`mp-dot ${tone}`} />
              <span className={`mp-word ${tone}`}>{word}</span>
              {word === 'RUNNING' && <span className="mp-fact">uptime 4h 12m</span>}
              {word === 'INDEXING' && <span className="mp-fact">2 jobs · 7 projects</span>}
            </div>

            {isRunning && (
              <div className="mp-heroline">
                <span className="mp-hero">:{PORT}</span>
                <span className="mp-caption">{networked ? 'reachable on your network' : 'localhost only'}</span>
              </div>
            )}
            {(phase === 'starting' || phase === 'restart') && (
              <div className="mp-sentence">Starting — loading the embedding model can take a few minutes on a cold start.</div>
            )}
            {word === 'STOPPED' && (
              <div className="mp-sentence">The server is not running. Search and the dashboard are unavailable.</div>
            )}

            {sweeping && (
              <div className="mp-progress">
                {Array.from({ length: SWEEP_CELLS }, (_, i) => (
                  <span key={i} className={`mp-cell ${((i - sweep + SWEEP_CELLS) % SWEEP_CELLS) < SWEEP_LIT ? 'on' : ''}`} />
                ))}
              </div>
            )}
          </header>

          <div className="mp-table mp-section">
            {rows.map(([k, v]) => (
              <span key={k} className="mp-row">
                <span className="mp-k">{k}</span>
                <span className="mp-v">{v}</span>
              </span>
            ))}
          </div>

          <div className="mp-actions mp-section">
            {busy || phase === 'starting' ? (
              <span className="mp-btn primary disabled">
                <span className="mp-load"><span /><span /><span /></span>
                {busy ? 'Applying the setting…' : 'Starting…'}
              </span>
            ) : isRunning ? (
              <>
                <span className="mp-btn destructive">Stop Server</span>
                <span className="mp-btn">Open Dashboard<span className="mp-ext">↗</span></span>
              </>
            ) : (
              <span className="mp-btn primary">Start Server</span>
            )}
          </div>

          <div className="mp-toggles mp-section">
            <span className={`mp-toggle ${networked ? 'on' : ''} ${phase === 'netclick' ? 'hit' : ''} ${sweeping || setup || busy ? 'off-limits' : ''}`}>
              <span className="mp-track"><span className="mp-knob" /></span>
              <span className="mp-lbl">
                <span className="mp-name">Allow network access</span>
                <span className="mp-hint">{networked ? LAN_ADDR : `127.0.0.1:${PORT} · this Mac only`}</span>
              </span>
            </span>
            <span className={`mp-toggle ${sweeping || setup || busy ? 'off-limits' : ''}`}>
              <span className="mp-track"><span className="mp-knob" /></span>
              <span className="mp-lbl"><span className="mp-name">Launch at login</span></span>
            </span>
          </div>

          <footer className="mp-ft mp-section">
            <span className="mp-link">Quit cix</span>
            <span className="mp-note">server keeps running</span>
            <span className="mp-spacer" />
            <span className="mp-link">Password…</span>
            <span className="mp-link">Updates…</span>
          </footer>

          <PanelDialog phase={phase} typed={typed} />
        </div>
      </div>

      <div className="mac-stage-foot">
        <span>first launch, start to finish · <a href="/docs/#install">what each control does →</a></span>
        <DemoControls
          running={running}
          onPlay={() => { setStarted(true); setTyped(''); setIdx(0); setRunning(true); }}
          onStop={() => { setStarted(true); setRunning(false); setTyped(EMAIL); setIdx(LAST); }}
        />
      </div>
    </div>
  );
}

// The cards the app puts up, in the order this demo needs them: the setup
// prompt, a busy card with no buttons, the credentials, and the confirmation
// that widening network access asks for. Same shapes cixDialog() renders.
function PanelDialog({ phase, typed }) {
  const asking = phase === 'ask' || phase === 'typing' || phase === 'ok';
  const confirming = phase === 'netask' || phase === 'netallow';
  if (!asking && !confirming && phase !== 'download' && phase !== 'creds') return null;

  return (
    <div className="mp-dialog">
      <div className="mp-dcard">
        {asking && (
          <>
            <div className="mp-dtitle">Set up cix</div>
            <div className="mp-dmsg">{SETUP_INTRO}</div>
            <div className={`mp-dinput ${phase === 'typing' ? 'focus' : ''}`}>
              {typed}
              {phase === 'typing' && <span className="mp-caret" />}
            </div>
            <div className="mp-dbtns">
              <span className="mp-btn">Cancel</span>
              <span className={`mp-btn primary ${phase === 'ok' ? 'pressed' : ''}`}>OK</span>
            </div>
          </>
        )}

        {phase === 'download' && (
          <>
            <div className="mp-dtitle">Downloading the cix server…</div>
            <div className="mp-dbusy"><span className="mp-load"><span /><span /><span /></span></div>
          </>
        )}

        {phase === 'creds' && (
          <>
            <div className="mp-dtitle">cix is set up</div>
            <div className="mp-dmsg">
              {`Sign in at http://localhost:${PORT}/dashboard\n\nEmail:\n${EMAIL}\n\nTemporary password:`}
            </div>
            <div className="mp-dsecret">{PASSWORD}</div>
            <div className="mp-dnote">The password is on your clipboard.</div>
            <div className="mp-dbtns">
              <span className="mp-btn primary">OK</span>
            </div>
          </>
        )}

        {confirming && (
          <>
            <div className="mp-dtitle">Allow access from your network?</div>
            <div className="mp-dmsg">{NETWORK_ASK}</div>
            <div className="mp-dbtns">
              <span className="mp-btn">Cancel</span>
              <span className={`mp-btn primary ${phase === 'netallow' ? 'pressed' : ''}`}>Allow</span>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
