import { useState, useEffect, useRef } from 'react';

// Two-pane workspace demo: an agent chat on the left, and the cix commands
// the agent actually runs on the right — synchronized. The point is the
// WORKFLOW: one minimal question → broad workspace search → several services
// implicated → targeted per-repo lookups → a concrete, actionable answer.
//
// This is a SCRIPTED scenario with abstract repos (acme-*), not transcribed
// output (unlike the hero terminal): a fictitious workspace keeps real
// project internals off the marketing page. The OUTPUT FORMAT mirrors the
// real CLI (cli/cmd/workspace.go renderSearch, cix symbols / cix search
// renderers) — keep it faithful when editing.

const REPOS = ['acme-gateway', 'acme-billing', 'acme-auth', 'acme-web'];

const USER_MSG = 'How do I add a new fee to checkout?';

const INTERIM = <>Fees touch <b>acme-billing</b>, <b>acme-gateway</b> and <b>acme-web</b>. Checking where fees are defined and how the gateway caches them…</>;

const REPLY = <>Add it in <b>acme-billing</b>: extend <code>FeeSchedule</code> (internal/fees/schedule.go:34) and register it with <code>RegisterFee</code>. Then flush the gateway's quote cache — <code>QuoteCache.Invalidate</code> (internal/pricing/cache.go:81) — or checkout keeps serving stale totals. <b>acme-web</b> picks the new fee up from <code>/v1/fees</code>, no change needed.</>;

// Step outputs use the same row vocabulary as the other terminals on the page.
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
    cmd: 'cix symbols Fee -n acme-billing',
    output: [
      { t: 'dim', text: 'Found 2 symbol(s):' },
      { t: 'sym', kind: '[type]', name: 'FeeSchedule' },
      { t: 'loc', text: 'internal/fees/schedule.go:34-58 (go)' },
      { t: 'sym', kind: '[function]', name: 'RegisterFee' },
      { t: 'loc', text: 'internal/fees/schedule.go:73-98 (go)' },
    ],
  },
  {
    cmd: 'cix search "fee quote cache" -n acme-gateway',
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
        T(() => advance(), 500);
      }
    } else if (phase === 'interim') {
      T(() => advance(), 1400);
    } else if (phase === 'reply') {
      T(() => setPhase('hold'), 400);
    } else if (phase === 'hold') {
      T(() => setPhase('wipe'), 9000);
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

  const replyVisible = phase === 'reply' || phase === 'hold' || phase === 'wipe';
  const thinking = !replyVisible && phase !== 'user';

  // terminal renders every finished step in full, plus the current one
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
        <div className="chat-win" role="img" aria-label="agent chat: the user asks one question, the agent investigates across repos and answers with concrete changes">
          <div className="chat-head">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">agent chat</span>
          </div>
          <div className="chat-body">
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
        <div className="term" role="img" aria-label="terminal: the sequence of cix commands the agent runs — a workspace-wide search, then targeted per-repo lookups">
          <div className="term-bar">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">what the agent runs</span>
          </div>
          <div className="term-body" style={{ minHeight: 440 }}>
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
