import { useState, useEffect, useRef } from 'react';

// Two-pane workspace demo: an agent chat on the left, and the cix commands
// the agent actually runs on the right — synchronized, both panes scroll
// like the real thing. The point is the WORKFLOW: one minimal question →
// broad workspace search → several services implicated → targeted per-repo
// lookups (including a dead end: no admin UI exists) → a concrete answer
// down to the SQL to run and the cache to flush.
//
// This is a SCRIPTED scenario with abstract repos (acme-*), not transcribed
// output (unlike the hero terminal): a fictitious workspace keeps real
// project internals off the marketing page. The OUTPUT FORMAT mirrors the
// real CLI (cli/cmd/workspace.go renderSearch, cix search / cix symbols
// renderers, and the literal "No results found" empty message) — keep it
// faithful when editing.

const REPOS = ['acme-gateway', 'acme-billing', 'acme-auth', 'acme-web'];

const USER_MSG = 'How do I add a new fee to checkout?';

const INTERIM = <>Fees touch <b>acme-billing</b>, <b>acme-gateway</b> and <b>acme-web</b>. Checking how fees are stored and whether there's an admin UI for them…</>;

const REPLY = <>
  Fees are <b>rows in the database</b>: <code>FeeSchedule</code> loads the <code>fees</code> table via <code>LoadSchedule</code> (fees/store.go:41); schema in <code>migrations/0007_create_fees.sql</code>. I found <b>no admin UI</b> for fees (nothing matches in acme-web), so add the row directly:
  <pre className="msg-code">{`INSERT INTO fees (code, label, amount_bps, active)
VALUES ('intl_card', 'Intl. card fee', 150, true);`}</pre>
  Then flush the gateway's quote cache — <code>QuoteCache.Invalidate</code> (pricing/cache.go:81) — or checkout keeps serving stale totals. acme-web renders whatever <code>/v1/fees</code> returns, no change needed.
</>;

const STEPS = [
  {
    cmd: 'cix ws platform search "fee calculation"',
    output: [
      { t: 'head', text: 'Top projects:' },
      { t: 'proj', score: '0.643', label: 'acme-billing', hits: '9 hits', bm25: '6.870', dense: '0.581', path: 'github.com/acme/acme-billing' },
      { t: 'proj', score: '0.512', label: 'acme-gateway', hits: '6 hits', bm25: '4.902', dense: '0.494', path: 'github.com/acme/acme-gateway' },
      { t: 'proj', score: '0.334', label: 'acme-web', hits: '2 hits', bm25: '1.428', dense: '0.371', path: 'github.com/acme/acme-web' },
      { t: 'dim', text: '  …' },
    ],
    interimAfter: true,
  },
  {
    cmd: 'cix search "fees table schema" -n acme-billing',
    output: [
      { t: 'dim', text: 'Found 2 file(s) (18.9ms):' },
      { t: 'file', text: '1. migrations/0007_create_fees.sql', meta: '[best 0.581] · 1 match · sql' },
      { t: 'chunkline', score: '0.581', lines: '1-14', sig: 'CREATE TABLE fees' },
      { t: 'file', text: '2. internal/fees/store.go', meta: '[best 0.522] · 1 match · go' },
      { t: 'chunkline', score: '0.522', lines: '41-77', sig: 'method LoadSchedule' },
    ],
  },
  {
    cmd: 'cix search "fees admin form" -n acme-web',
    output: [
      { t: 'dim', text: 'No results found' },
    ],
  },
  {
    cmd: 'cix search "quote cache invalidate" -n acme-gateway',
    output: [
      { t: 'dim', text: 'Found 1 file(s) (21.4ms):' },
      { t: 'file', text: '1. internal/pricing/cache.go', meta: '[best 0.573] · 2 matches · go' },
      { t: 'chunkline', score: '0.573', lines: '81-112', sig: 'method Invalidate' },
      { t: 'chunkline', score: '0.488', lines: '29-61', sig: 'type QuoteCache' },
    ],
  },
];

// phases: user → think → (cmd → out [→ interim])×steps → reply → hold → wipe
export function WorkspaceDemo() {
  const [stepIdx, setStepIdx] = useState(0);
  const [phase, setPhase] = useState('user');
  const [userTyped, setUserTyped] = useState('');
  const [cmdTyped, setCmdTyped] = useState('');
  const [outShown, setOutShown] = useState(0);
  const [interimShown, setInterimShown] = useState(false);
  const timeoutsRef = useRef([]);
  const termRef = useRef(null);
  const chatRef = useRef(null);

  const step = STEPS[stepIdx];

  useEffect(() => {
    const clear = () => {
      timeoutsRef.current.forEach(clearTimeout);
      timeoutsRef.current = [];
    };
    const T = (fn, ms) => { const id = setTimeout(fn, ms); timeoutsRef.current.push(id); };

    if (phase === 'user') {
      if (userTyped.length < USER_MSG.length) {
        T(() => setUserTyped(USER_MSG.slice(0, userTyped.length + 1)), 24);
      } else {
        T(() => setPhase('think'), 350);
      }
    } else if (phase === 'think') {
      T(() => setPhase('cmd'), 850);
    } else if (phase === 'cmd') {
      if (cmdTyped.length < step.cmd.length) {
        T(() => setCmdTyped(step.cmd.slice(0, cmdTyped.length + 1)), 24);
      } else {
        T(() => setPhase('out'), 300);
      }
    } else if (phase === 'out') {
      if (outShown < step.output.length) {
        T(() => setOutShown(outShown + 1), 190);
      } else if (step.interimAfter && !interimShown) {
        T(() => { setInterimShown(true); setPhase('interim'); }, 350);
      } else {
        T(() => advance(), 600);
      }
    } else if (phase === 'interim') {
      T(() => advance(), 1400);
    } else if (phase === 'reply') {
      T(() => setPhase('hold'), 400);
    } else if (phase === 'hold') {
      T(() => setPhase('wipe'), 11000);
    } else if (phase === 'wipe') {
      T(() => {
        setUserTyped('');
        setCmdTyped('');
        setOutShown(0);
        setInterimShown(false);
        setStepIdx(0);
        setPhase('user');
      }, 300);
    }
    return clear;

    function advance() {
      if (stepIdx < STEPS.length - 1) {
        setStepIdx(stepIdx + 1);
        setCmdTyped('');
        setOutShown(0);
        setPhase('cmd');
      } else {
        setPhase('reply');
      }
    }
  }, [phase, userTyped, cmdTyped, outShown, stepIdx, interimShown]);

  // keep both panes pinned to the bottom as content streams in,
  // exactly like a live terminal / chat
  useEffect(() => {
    if (termRef.current) termRef.current.scrollTop = termRef.current.scrollHeight;
    if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight;
  });

  const replyVisible = phase === 'reply' || phase === 'hold' || phase === 'wipe';
  const thinking = !replyVisible && phase !== 'user';

  const doneSteps = STEPS.slice(0, stepIdx);
  const currentTyping = phase === 'cmd';
  const currentOut = phase === 'out' || phase === 'interim' ? outShown
    : (phase === 'cmd' ? 0 : step.output.length);

  return (
    <div>
      <div className="repo-chips" aria-label="example workspace named platform">
        <span className="repo-chips-label">workspace <b>platform</b> ·</span>
        {REPOS.map(r => <span key={r} className="repo-chip">{r}</span>)}
        <span className="repo-chips-label">· any number of repos</span>
      </div>

      <div className="ws-demo">
        {/* left: agent chat */}
        <div className="chat-win" role="img" aria-label="agent chat: the user asks one question, the agent investigates across repos and answers with the exact SQL to run">
          <div className="chat-head">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">agent chat</span>
          </div>
          <div className="chat-body" ref={chatRef}>
            {userTyped && (
              <div className="msg msg-user">
                {userTyped}
                {phase === 'user' && <span className="cursor" />}
              </div>
            )}
            {interimShown && (
              <div className="msg msg-agent">{INTERIM}</div>
            )}
            {thinking && (
              <div className="msg msg-agent msg-thinking" aria-hidden>
                <span /><span /><span />
              </div>
            )}
            {replyVisible && (
              <div className="msg msg-agent">{REPLY}</div>
            )}
          </div>
        </div>

        {/* right: what the agent actually runs */}
        <div className="term" role="img" aria-label="terminal: the sequence of cix commands the agent runs — a workspace-wide search, then targeted per-repo lookups, including one that finds nothing">
          <div className="term-bar">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">what the agent runs</span>
          </div>
          <div className="term-body ws-term-body" ref={termRef}>
            {doneSteps.map((d, di) => (
              <div key={di} style={{ marginBottom: 14 }}>
                <CmdLine text={d.cmd} />
                <StepOut rows={d.output} upTo={d.output.length} />
              </div>
            ))}
            {(phase !== 'user' && phase !== 'think') && (
              <div>
                <CmdLine text={cmdTyped} cursor={currentTyping} />
                <StepOut rows={step.output} upTo={currentOut} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function CmdLine({ text, cursor }) {
  return (
    <div>
      <span className="prompt">$</span>{' '}
      <span className="green">cix </span>
      <span className="cmd">{text.replace(/^cix\s+/, '')}</span>
      {cursor && <span className="cursor" />}
    </div>
  );
}

function StepOut({ rows, upTo }) {
  if (upTo === 0) return null;
  return (
    <div style={{ marginTop: 8 }}>
      {rows.slice(0, upTo).map((row, i) => (
        <div className="term-row" key={i}>
          {row.t === 'head' && <div className="dim">{row.text}</div>}
          {row.t === 'dim' && <div className="dim">{row.text}</div>}
          {row.t === 'proj' && (
            <div>
              {'  '}<span className="score">[{row.score}]</span> <span className="blue">{row.label}</span>
              <span className="dim">  — {row.hits} · bm25 {row.bm25} · dense {row.dense} · </span>
              <span className="path">{row.path}</span>
            </div>
          )}
          {row.t === 'sym' && (
            <div>{'  '}<span className="dim">{row.kind}</span> <span className="blue">{row.name}</span></div>
          )}
          {row.t === 'loc' && (
            <div className="dim">{'    '}<span className="path">{row.text}</span></div>
          )}
          {row.t === 'file' && (
            <div><span className="path">{row.text}</span><span className="dim">  {row.meta}</span></div>
          )}
          {row.t === 'chunkline' && (
            <div>{'   '}<span className="score">[{row.score}]</span><span className="dim"> lines {row.lines}  </span><span className="blue">{row.sig}</span></div>
          )}
        </div>
      ))}
    </div>
  );
}
