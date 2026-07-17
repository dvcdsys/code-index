import { useState, useEffect, useRef } from 'react';
import { DemoControls } from '../shared/demo-controls.jsx';

// Two-pane workspace demo: an agent chat on the left, and the cix commands
// the agent actually runs on the right — synchronized, both panes scroll
// like the real thing. The point is the WORKFLOW: one minimal question →
// ONE broad workspace search that already surfaces most of the relevant
// material across repos (storage AND the gateway cache) → one deep dive for
// the code bodies → one honest dead end (no admin UI) → a concrete answer
// down to the SQL to run.
//
// This is a SCRIPTED scenario with abstract repos (acme-*), not transcribed
// output (unlike the hero terminal): a fictitious workspace keeps real
// project internals off the marketing page. The OUTPUT FORMAT mirrors the
// real CLI — cli/cmd/workspace.go renderSearch (Top projects / Top chunks
// with project: and symbol: lines, no code bodies); cix search chunk output
// with `-- [score] lines N-M (kind)` heads and fenced code bodies; the
// literal "No results found" empty message. Keep it faithful when editing.
// The CREATE TABLE body is the point: it is where the agent's INSERT
// columns visibly come from.

const REPOS = ['acme-gateway', 'acme-billing', 'acme-auth', 'acme-web'];

// The leading slash-command is the cix plugin's real cross-repo research
// skill — invoking it is how a Claude Code user actually starts this flow.
const SLASH_CMD = '/cix-workspace';
const USER_MSG = `${SLASH_CMD} using the platform workspace, find how do I add a new fee to checkout?`;

const INTERIM = <>One search maps it: fees load from a database table in <b>acme-billing</b> (<code>LoadSchedule</code>), and <b>acme-gateway</b> caches computed quotes (<code>QuoteCache.Invalidate</code>). Pulling the schema and checking for an admin UI…</>;

const REPLY = <>
  Fees are <b>rows in the database</b>: <code>FeeSchedule</code> loads the <code>fees</code> table via <code>LoadSchedule</code> (fees/store.go:41); schema in <code>migrations/0007_create_fees.sql</code>. I found <b>no admin UI</b> for fees (nothing matches in acme-web), so add the row directly:
  <pre className="msg-code">{`INSERT INTO fees (code, label, amount_bps, active)
VALUES ('intl_card', 'Intl. card fee', 150, true);`}</pre>
  Then flush the gateway's quote cache — <code>QuoteCache.Invalidate</code> (pricing/cache.go:81, the workspace search already surfaced it) — or checkout keeps serving stale totals. acme-web renders whatever <code>/v1/fees</code> returns, no change needed.
</>;

const STEPS = [
  {
    cmd: 'cix ws platform search "fee calculation"',
    output: [
      { t: 'head', text: 'Top projects:' },
      { t: 'proj', score: '0.643', label: 'acme-billing', hits: '9 hits', bm25: '6.870', dense: '0.581', path: 'github.com/acme/acme-billing' },
      { t: 'proj', score: '0.512', label: 'acme-gateway', hits: '6 hits', bm25: '4.902', dense: '0.494', path: 'github.com/acme/acme-gateway' },
      { t: 'proj', score: '0.334', label: 'acme-web', hits: '2 hits', bm25: '1.428', dense: '0.371', path: 'github.com/acme/acme-web' },
      { t: 'head', text: 'Top chunks:', gap: true },
      { t: 'wchunk', score: '0.631', loc: 'internal/fees/store.go:41-58' },
      { t: 'wmeta', text: 'project: github.com/acme/acme-billing' },
      { t: 'wsym', name: 'LoadSchedule' },
      { t: 'wchunk', score: '0.573', loc: 'internal/pricing/cache.go:81-96', gap: true },
      { t: 'wmeta', text: 'project: github.com/acme/acme-gateway' },
      { t: 'wsym', name: 'QuoteCache.Invalidate' },
      { t: 'wchunk', score: '0.548', loc: 'migrations/0007_create_fees.sql:1-9', gap: true },
      { t: 'wmeta', text: 'project: github.com/acme/acme-billing' },
      { t: 'dim', text: '  …' },
    ],
    interimAfter: true,
  },
  {
    cmd: 'cix search "fees table schema" -n acme-billing',
    output: [
      { t: 'dim', text: 'Found 2 file(s) (18.9ms):' },
      { t: 'file', text: '1. migrations/0007_create_fees.sql', meta: '[best 0.581] · 1 match · sql' },
      { t: 'chunkhead', score: '0.581', lines: '1-9', kind: 'module' },
      { t: 'code', lang: 'sql', lines: [
        'CREATE TABLE fees (',
        '    code        TEXT PRIMARY KEY,',
        '    label       TEXT NOT NULL,',
        '    amount_bps  INTEGER NOT NULL, -- basis points',
        '    active      BOOLEAN NOT NULL DEFAULT true',
        ');',
      ]},
      { t: 'file', text: '2. internal/fees/store.go', meta: '[best 0.522] · 1 match · go' },
      { t: 'chunkhead', score: '0.522', lines: '41-58', kind: 'method LoadSchedule' },
      { t: 'code', lang: 'go', lines: [
        '// LoadSchedule builds the fee schedule checkout uses.',
        '// Only active fees are applied.',
        'func (s *Store) LoadSchedule(ctx context.Context) (*FeeSchedule, error) {',
        '    rows, err := s.db.QueryContext(ctx,',
        '        `SELECT code, label, amount_bps',
        '           FROM fees WHERE active`)',
        '    …',
      ]},
    ],
  },
  {
    cmd: 'cix search "fees admin form" -n acme-web',
    output: [
      { t: 'dim', text: 'No results found' },
    ],
  },
];

const LAST = STEPS.length - 1;

// phases: user → think → (cmd → out [→ interim])×steps → reply → hold → wipe
export function WorkspaceDemo() {
  const [running, setRunning] = useState(true);
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
    if (!running) return clear;
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
        T(() => setOutShown(outShown + 1), 180);
      } else if (step.interimAfter && !interimShown) {
        T(() => { setInterimShown(true); setPhase('interim'); }, 350);
      } else {
        T(() => advance(), 600);
      }
    } else if (phase === 'interim') {
      T(() => advance(), 1500);
    } else if (phase === 'reply') {
      T(() => setPhase('hold'), 400);
    } else if (phase === 'hold') {
      T(() => setPhase('wipe'), 12000);
    } else if (phase === 'wipe') {
      T(() => reset('user'), 300);
    }
    return clear;

    function advance() {
      if (stepIdx < LAST) {
        setStepIdx(stepIdx + 1);
        setCmdTyped('');
        setOutShown(0);
        setPhase('cmd');
      } else {
        setPhase('reply');
      }
    }
  }, [running, phase, userTyped, cmdTyped, outShown, stepIdx, interimShown]);

  function reset(startPhase) {
    setUserTyped('');
    setCmdTyped('');
    setOutShown(0);
    setInterimShown(false);
    setStepIdx(0);
    setPhase(startPhase);
  }

  // ⏹ — skip straight to the completed session
  function stop() {
    setRunning(false);
    setUserTyped(USER_MSG);
    setInterimShown(true);
    setStepIdx(LAST);
    setCmdTyped(STEPS[LAST].cmd);
    setOutShown(STEPS[LAST].output.length);
    setPhase('hold');
  }

  // ▶ — run the loop again from the top
  function play() {
    reset('user');
    setRunning(true);
  }

  // keep both panes pinned to the bottom as content streams in,
  // exactly like a live terminal / chat
  useEffect(() => {
    if (termRef.current) termRef.current.scrollTop = termRef.current.scrollHeight;
    if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight;
  });

  const replyVisible = phase === 'reply' || phase === 'hold' || phase === 'wipe';
  const thinking = running && !replyVisible && phase !== 'user';

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
                <span className="msg-slash">{userTyped.slice(0, SLASH_CMD.length)}</span>
                {userTyped.slice(SLASH_CMD.length)}
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
        <div className="term">
          <div className="term-bar">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">what the agent runs</span>
            <DemoControls running={running} onPlay={play} onStop={stop} />
          </div>
          <div className="term-body ws-term-body" ref={termRef} role="img" aria-label="terminal: one workspace-wide search surfaces storage and cache at once, then a deep dive and a dead-end check">
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
        <div className="term-row" key={i} style={row.gap ? { marginTop: 10 } : undefined}>
          {row.t === 'head' && <div className="dim">{row.text}</div>}
          {row.t === 'dim' && <div className="dim">{row.text}</div>}
          {row.t === 'proj' && (
            <div>
              {'  '}<span className="score">[{row.score}]</span> <span className="blue">{row.label}</span>
              <span className="dim">  — {row.hits} · bm25 {row.bm25} · dense {row.dense} · </span>
              <span className="path">{row.path}</span>
            </div>
          )}
          {row.t === 'wchunk' && (
            <div>{'  '}<span className="score">[{row.score}]</span> <span className="path">{row.loc}</span></div>
          )}
          {row.t === 'wmeta' && (
            <div className="dim">{'         ' + row.text}</div>
          )}
          {row.t === 'wsym' && (
            <div>{'         '}<span className="dim">symbol:  </span><span className="blue">{row.name}</span></div>
          )}
          {row.t === 'file' && (
            <div><span className="path">{row.text}</span><span className="dim">  {row.meta}</span></div>
          )}
          {row.t === 'chunkhead' && (
            <div>{'   '}<span className="dim">-- </span><span className="score">[{row.score}]</span><span className="dim"> lines {row.lines}  (</span><span className="blue">{row.kind}</span><span className="dim">)</span></div>
          )}
          {row.t === 'code' && (
            <div>
              <div className="dim">{'      ```' + row.lang}</div>
              {row.lines.map((l, j) => (
                <div key={j} className={l.trimStart().startsWith('//') || l.includes('-- basis') ? 'dim' : ''}>{'      ' + l}</div>
              ))}
              <div className="dim">{'      ```'}</div>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
