import { useState, useEffect, useRef } from 'react';

// Two-pane workspace demo: an agent chat on the left, and the cix commands
// the agent actually runs on the right — synchronized.
//
// This is a SCRIPTED scenario with abstract repos (acme-*), not transcribed
// output (unlike the hero terminal): a fictitious 4-repo workspace keeps
// real project internals off the marketing page. The OUTPUT FORMAT is the
// real CLI's — mirror cli/cmd/workspace.go renderSearch() ("Top projects:"
// panel with bm25/dense signals, then "Top chunks:" with project + symbol
// lines) when editing.

const REPOS = ['acme-gateway', 'acme-billing', 'acme-auth', 'acme-web'];

const SCENARIOS = [
  {
    user: 'Where do we retry failed card charges? Check the platform workspace.',
    cmd: 'cix ws platform search "retry failed card charges"',
    output: [
      { t: 'head', text: 'Top projects:' },
      { t: 'proj', score: '0.642', label: 'acme-billing', hits: '11 hits', bm25: '7.120', dense: '0.581', path: 'github.com/acme/acme-billing' },
      { t: 'proj', score: '0.418', label: 'acme-gateway', hits: '4 hits', bm25: '3.981', dense: '0.442', path: 'github.com/acme/acme-gateway' },
      { t: 'proj', score: '0.201', label: 'acme-web', hits: '1 hit', bm25: '0.870', dense: '0.311', path: 'github.com/acme/acme-web' },
      { t: 'head', text: 'Top chunks:', gap: true },
      { t: 'chunk', score: '0.631', loc: 'internal/payments/retry.go:52-96', proj: 'github.com/acme/acme-billing', sym: 'RetryFailedCharges' },
      { t: 'chunk', score: '0.574', loc: 'internal/payments/stripe.go:141-183', proj: 'github.com/acme/acme-billing', sym: 'ChargeCard' },
      { t: 'chunk', score: '0.446', loc: 'internal/checkout/handler.go:210-244', proj: 'github.com/acme/acme-gateway', sym: 'handleCheckout' },
    ],
    reply: <>Retries live in <b>acme-billing</b>: <code>RetryFailedCharges</code> (internal/payments/retry.go:52) drains a queue with exponential backoff. It's fed by failed <code>ChargeCard</code> calls (stripe.go:141), which enter through <code>handleCheckout</code> in <b>acme-gateway</b>.</>,
  },
  {
    user: 'Which services validate webhook signatures? Same workspace.',
    cmd: 'cix ws platform search "validate webhook signatures"',
    output: [
      { t: 'head', text: 'Top projects:' },
      { t: 'proj', score: '0.588', label: 'acme-gateway', hits: '7 hits', bm25: '6.204', dense: '0.552', path: 'github.com/acme/acme-gateway' },
      { t: 'proj', score: '0.483', label: 'acme-billing', hits: '5 hits', bm25: '4.117', dense: '0.507', path: 'github.com/acme/acme-billing' },
      { t: 'head', text: 'Top chunks:', gap: true },
      { t: 'chunk', score: '0.612', loc: 'internal/webhooks/verify.go:24-61', proj: 'github.com/acme/acme-gateway', sym: 'VerifySignature' },
      { t: 'chunk', score: '0.549', loc: 'internal/stripe/webhook.go:88-130', proj: 'github.com/acme/acme-billing', sym: 'validateStripeEvent' },
    ],
    reply: <>Two places: <b>acme-gateway</b> has the generic HMAC check in <code>VerifySignature</code> (webhooks/verify.go:24); <b>acme-billing</b> separately validates Stripe events in <code>validateStripeEvent</code> (stripe/webhook.go:88). <b>acme-auth</b> and <b>acme-web</b> don't touch webhooks.</>,
  },
];

// phases: user → think → cmd → out → reply → hold → wipe
export function WorkspaceDemo() {
  const [sIdx, setSIdx] = useState(0);
  const [phase, setPhase] = useState('user');
  const [userTyped, setUserTyped] = useState('');
  const [cmdTyped, setCmdTyped] = useState('');
  const [outShown, setOutShown] = useState(0);
  const timeoutsRef = useRef([]);

  const s = SCENARIOS[sIdx];

  useEffect(() => {
    const clear = () => {
      timeoutsRef.current.forEach(clearTimeout);
      timeoutsRef.current = [];
    };
    const T = (fn, ms) => { const id = setTimeout(fn, ms); timeoutsRef.current.push(id); };

    if (phase === 'user') {
      if (userTyped.length < s.user.length) {
        T(() => setUserTyped(s.user.slice(0, userTyped.length + 1)), 22);
      } else {
        T(() => setPhase('think'), 350);
      }
    } else if (phase === 'think') {
      T(() => setPhase('cmd'), 900);
    } else if (phase === 'cmd') {
      if (cmdTyped.length < s.cmd.length) {
        T(() => setCmdTyped(s.cmd.slice(0, cmdTyped.length + 1)), 26);
      } else {
        T(() => setPhase('out'), 320);
      }
    } else if (phase === 'out') {
      if (outShown < s.output.length) {
        T(() => setOutShown(outShown + 1), 210);
      } else {
        T(() => setPhase('reply'), 380);
      }
    } else if (phase === 'reply') {
      T(() => setPhase('hold'), 400);
    } else if (phase === 'hold') {
      T(() => setPhase('wipe'), 6200);
    } else if (phase === 'wipe') {
      T(() => {
        setUserTyped('');
        setCmdTyped('');
        setOutShown(0);
        setSIdx((sIdx + 1) % SCENARIOS.length);
        setPhase('user');
      }, 300);
    }
    return clear;
  }, [phase, userTyped, cmdTyped, outShown, sIdx]);

  const replyVisible = phase === 'reply' || phase === 'hold' || phase === 'wipe';

  return (
    <div>
      <div className="repo-chips" aria-label="workspace platform — four repositories">
        <span className="repo-chips-label">workspace <b>platform</b> ·</span>
        {REPOS.map(r => <span key={r} className="repo-chip">{r}</span>)}
      </div>

      <div className="ws-demo">
        {/* left: agent chat */}
        <div className="chat-win" role="img" aria-label="agent chat: the user asks a cross-repo question, the agent answers from workspace search results">
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
            {(phase === 'think' || phase === 'cmd' || phase === 'out') && (
              <div className="msg msg-agent msg-thinking" aria-hidden>
                <span /><span /><span />
              </div>
            )}
            {replyVisible && (
              <div className="msg msg-agent">{s.reply}</div>
            )}
          </div>
        </div>

        {/* right: what the agent actually runs */}
        <div className="term" role="img" aria-label="terminal: the cix workspace search command the agent runs, with ranked projects and chunks">
          <div className="term-bar">
            <span className="dot r" /><span className="dot y" /><span className="dot g" />
            <span className="title">what the agent runs</span>
          </div>
          <div className="term-body" style={{ minHeight: 380 }}>
            {(phase !== 'user' && phase !== 'think') && (
              <div>
                <span className="prompt">$</span>{' '}
                <span className="green">cix </span>
                <span className="cmd">{cmdTyped.replace(/^cix\s+/, '')}</span>
                {phase === 'cmd' && <span className="cursor" />}
              </div>
            )}
            {outShown > 0 && (
              <div style={{ marginTop: 12 }}>
                {s.output.slice(0, outShown).map((row, i) => (
                  <div className="term-row" key={`${sIdx}-${i}`} style={{ marginTop: row.gap ? 12 : 0 }}>
                    {row.t === 'head' && <div className="dim">{row.text}</div>}
                    {row.t === 'proj' && (
                      <div>
                        {'  '}<span className="score">[{row.score}]</span> <span className="blue">{row.label}</span>
                        <span className="dim">  — {row.hits} · bm25 {row.bm25} · dense {row.dense} · </span>
                        <span className="path">{row.path}</span>
                      </div>
                    )}
                    {row.t === 'chunk' && (
                      <>
                        <div>{'  '}<span className="score">[{row.score}]</span> <span className="path">{row.loc}</span></div>
                        <div className="dim">{'         '}project: {row.proj}</div>
                        <div>{'         '}<span className="dim">symbol:  </span><span className="blue">{row.sym}</span></div>
                      </>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
