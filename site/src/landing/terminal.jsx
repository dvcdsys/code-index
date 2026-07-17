import { useState, useEffect, useRef } from 'react';
import { DemoControls } from '../shared/demo-controls.jsx';

// Animated hero terminal. Every query below was actually run with `cix search`
// against this repository — scores, paths, line numbers, timings and symbol
// kinds are transcribed from real output, not invented. Re-verify after big
// indexer changes (the timings will drift; the symbols shouldn't).
const HERO_QUERIES = [
  {
    cmd: 'cix search "authentication middleware"',
    results: [
      { score: 0.52, path: 'server/internal/httpapi/middleware.go', line: 87,
        sym: 'type authContext struct', kind: 'type' },
      { score: 0.50, path: 'server/internal/httpapi/middleware.go', line: 103,
        sym: 'func requireAuth(d Deps) func(http.Handler) http.Handler', kind: 'func' },
      { score: 0.43, path: 'server/internal/httpapi/auth.go', line: 156,
        sym: 'Login', kind: 'method' },
    ],
    summary: '3 files (47.8ms)',
  },
  {
    cmd: 'cix search "watch for file changes and debounce"',
    results: [
      { score: 0.59, path: 'cli/internal/watcher/watcher.go', line: 330,
        sym: 'func (w *Watcher) trackChange(path string)', kind: 'method' },
      { score: 0.46, path: 'cli/internal/watcher/watcher.go', line: 163,
        sym: 'Start', kind: 'method' },
      { score: 0.45, path: 'cli/internal/watcher/watcher.go', line: 32,
        sym: 'type Watcher struct', kind: 'type' },
    ],
    summary: '2 files (24.2ms)',
  },
  {
    cmd: 'cix search "stream index files to the server in batches"',
    results: [
      { score: 0.57, path: 'server/internal/httpapi/indexing.go', line: 114,
        sym: 'func indexFilesStreamingHandler(…)', kind: 'func' },
      { score: 0.53, path: 'server/internal/httpapi/server.go', line: 837,
        sym: 'IndexFiles', kind: 'method' },
      { score: 0.52, path: 'server/internal/httpapi/openapi/openapi.gen.go', line: 3594,
        sym: 'IndexFiles', kind: 'method' },
    ],
    summary: '4 files (8.7ms)',
  },
];

export function HeroTerminal() {
  const [running, setRunning] = useState(true);
  const [phase, setPhase] = useState('typing'); // typing | streaming | hold | wipe
  const [qIdx, setQIdx] = useState(0);
  const [typed, setTyped] = useState('');
  const [shown, setShown] = useState(0);
  const timeoutsRef = useRef([]);

  const q = HERO_QUERIES[qIdx];

  // ⏹ — freeze on the current query, fully rendered
  function stop() {
    setRunning(false);
    setTyped(q.cmd);
    setShown(q.results.length);
    setPhase('hold');
  }

  // ▶ — resume cycling from the next query
  function play() {
    setTyped('');
    setShown(0);
    setQIdx((qIdx + 1) % HERO_QUERIES.length);
    setPhase('typing');
    setRunning(true);
  }

  useEffect(() => {
    const clear = () => {
      timeoutsRef.current.forEach(clearTimeout);
      timeoutsRef.current = [];
    };
    if (!running) return clear;
    const T = (fn, ms) => { const id = setTimeout(fn, ms); timeoutsRef.current.push(id); };

    if (phase === 'typing') {
      if (typed.length < q.cmd.length) {
        T(() => setTyped(q.cmd.slice(0, typed.length + 1)), 38);
      } else {
        T(() => setPhase('streaming'), 380);
      }
    } else if (phase === 'streaming') {
      if (shown < q.results.length) {
        T(() => setShown(shown + 1), 240);
      } else {
        T(() => setPhase('hold'), 420);
      }
    } else if (phase === 'hold') {
      T(() => setPhase('wipe'), 4200);
    } else if (phase === 'wipe') {
      T(() => {
        setTyped('');
        setShown(0);
        setQIdx((qIdx + 1) % HERO_QUERIES.length);
        setPhase('typing');
      }, 280);
    }
    return clear;
  }, [running, phase, typed, shown, qIdx]);

  return (
    <div className="term">
      <div className="term-bar">
        <span className="dot r" />
        <span className="dot y" />
        <span className="dot g" />
        <span className="title">~/code/code-index — cix search</span>
        <DemoControls running={running} onPlay={play} onStop={stop} />
      </div>
      <div className="term-body" role="img" aria-label="cix search demonstration — real output recorded from cix searching its own repository">
        <div>
          <span className="prompt">$</span>{' '}
          <span className="cmd">
            {renderCmd(typed)}
          </span>
          {phase === 'typing' && <span className="cursor" />}
        </div>

        {(phase === 'streaming' || phase === 'hold' || phase === 'wipe') && shown > 0 && (
          <div style={{ marginTop: 14 }}>
            {q.results.slice(0, shown).map((r, i) => (
              <div className="term-row" key={`${qIdx}-${i}`} style={{ marginBottom: 10, animationDelay: `${i * 0.05}s` }}>
                <div>
                  <span className="score">{r.score.toFixed(2)}</span>
                  <span className="dim"> · </span>
                  <span className="path">{r.path}</span>
                  <span className="dim">:{r.line}</span>
                </div>
                <div style={{ paddingLeft: 18 }}>
                  <span className="dim">{kindLabel(r.kind)} </span>
                  <span className="blue">{r.sym}</span>
                </div>
              </div>
            ))}
            {phase !== 'streaming' && shown === q.results.length && (
              <div className="term-row dim" style={{ marginTop: 10 }}>— Found {q.summary} · min-score 0.4</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function renderCmd(s) {
  // split: cix search "..." [flags]
  const m = s.match(/^(cix\s+)?(search\s*)?(.*)$/);
  if (!m) return s;
  const [, c, sub, rest] = m;
  return (
    <>
      <span className="green">{c || ''}</span>
      <span className="cmd">{sub || ''}</span>
      <span className="quote">{rest}</span>
    </>
  );
}

function kindLabel(k) {
  switch (k) {
    case 'func': return 'fn    ';
    case 'method': return 'method';
    case 'type': return 'type  ';
    default: return '      ';
  }
}
