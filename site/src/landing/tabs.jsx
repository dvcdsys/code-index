import { useState } from 'react';

// CLI tour — five modes side-by-side. Every output block below is transcribed
// from a real run against this repository (cix's own source). Formats mirror
// the actual CLI: search prints per-file groups with chunk scores; symbols /
// definitions print kind + location + signature; references and files print
// numbered lists.
const CLI_TABS = [
  {
    key: 'search',
    label: 'cix search',
    desc: <>Natural-language semantic search. Returns ranked files with per-chunk scores, line ranges and symbols. Replaces <b>Grep</b> for everything you can't quote verbatim.</>,
    cmd: 'cix search "authentication middleware"',
    output: [
      { type: 'sum', text: 'Found 3 file(s) (47.8ms):' },
      { type: 'file', path: 'server/internal/httpapi/middleware.go', meta: '[best 0.52] · 5 matches · go' },
      { type: 'chunk', score: '0.52', lines: '87-92', sig: 'type authContext' },
      { type: 'chunk', score: '0.50', lines: '103-163', sig: 'function requireAuth' },
      { type: 'file', path: 'server/internal/httpapi/auth.go', meta: '[best 0.43] · 2 matches · go' },
      { type: 'chunk', score: '0.42', lines: '156-205', sig: 'method Login' },
    ],
  },
  {
    key: 'symbols',
    label: 'cix symbols',
    desc: <>Fast lookup by name across functions, classes, methods, types. Built on the tree-sitter symbol index — exact, not embedding-based.</>,
    cmd: 'cix symbols Watcher --kind type',
    output: [
      { type: 'sum', text: 'Found 1 symbol(s):' },
      { type: 'sym', kind: '[type]', name: 'Watcher' },
      { type: 'loc', path: 'cli/internal/watcher/watcher.go', lines: '32-61 (go)' },
      { type: 'sig', text: 'type Watcher struct {' },
    ],
  },
  {
    key: 'definitions',
    label: 'cix definitions',
    desc: <>Where is this thing actually defined? Like <code>goto-def</code> in your IDE — for any agent with a shell. Alias: <code>cix def</code>.</>,
    cmd: 'cix def requireAuth',
    output: [
      { type: 'sum', text: "Found 1 definition(s) for 'requireAuth':" },
      { type: 'sym', kind: '[function]', name: 'requireAuth' },
      { type: 'loc', path: 'server/internal/httpapi/middleware.go', lines: '103-163 (go)' },
      { type: 'sig', text: 'func requireAuth(d Deps) func(http.Handler) http.Handler {' },
    ],
  },
  {
    key: 'references',
    label: 'cix references',
    desc: <>Every callsite, every test. <b>find-references</b> the agent reads in one round-trip. Alias: <code>cix refs</code>.</>,
    cmd: 'cix refs SendFilesStreaming',
    output: [
      { type: 'sum', text: "Found 10 reference(s) for 'SendFilesStreaming':" },
      { type: 'ref', n: '1.', path: 'cli/internal/client/index.go', lines: '111' },
      { type: 'ref', n: '2.', path: 'cli/internal/client/index_streaming_test.go', lines: '71' },
      { type: 'ref', n: '3.', path: 'cli/internal/client/index_streaming_test.go', lines: '107' },
      { type: 'sum', text: '…' },
    ],
  },
  {
    key: 'files',
    label: 'cix files',
    desc: <>Path-pattern file search, scoped to indexed files only. Tiny, but agents love it.</>,
    cmd: 'cix files "openapi"',
    output: [
      { type: 'sum', text: 'Found 5 file(s):' },
      { type: 'ref', n: '1.', path: 'doc/openapi.yaml', meta: '(yaml)' },
      { type: 'ref', n: '2.', path: 'server/internal/httpapi/openapi/gen.go', meta: '(go)' },
      { type: 'ref', n: '3.', path: 'server/internal/httpapi/openapi/oapi.yaml', meta: '(yaml)' },
      { type: 'ref', n: '4.', path: 'server/internal/httpapi/openapi/openapi.gen.go', meta: '(go)' },
    ],
  },
];

export function CLITabs() {
  const [active, setActive] = useState(CLI_TABS[0].key);
  const tab = CLI_TABS.find(t => t.key === active);
  return (
    <div className="tabs">
      <div className="tab-bar" role="tablist">
        {CLI_TABS.map(t => (
          <button
            key={t.key}
            role="tab"
            aria-selected={t.key === active}
            className={`tab-btn ${t.key === active ? 'active' : ''}`}
            onClick={() => setActive(t.key)}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="tab-body">
        <div className="term">
          <div className="term-body" key={active}>
            <div>
              <span className="prompt">$</span>{' '}
              <span className="green">cix </span>
              <span className="cmd">{tab.cmd.replace(/^cix\s+/, '').split(' ')[0]} </span>
              <span className="quote">{tab.cmd.replace(/^cix\s+\S+\s+/, '')}</span>
            </div>
            <div style={{ marginTop: 14 }}>
              {tab.output.map((row, i) => (
                <div className="term-row" key={i} style={{ marginBottom: 6, animationDelay: `${i * 0.04}s` }}>
                  {row.type === 'file' && (
                    <div style={{ marginTop: 6 }}>
                      <span className="path">{row.path}</span>
                      <span className="dim">  {row.meta}</span>
                    </div>
                  )}
                  {row.type === 'chunk' && (
                    <div style={{ paddingLeft: 18 }}>
                      <span className="score">{row.score}</span>
                      <span className="dim"> lines {row.lines}  </span>
                      <span className="blue">{row.sig}</span>
                    </div>
                  )}
                  {row.type === 'sym' && (
                    <div style={{ marginTop: 6 }}>
                      <span className="dim">{row.kind} </span>
                      <span className="blue">{row.name}</span>
                    </div>
                  )}
                  {row.type === 'loc' && (
                    <div style={{ paddingLeft: 18 }}>
                      <span className="path">{row.path}</span>
                      <span className="dim">:{row.lines}</span>
                    </div>
                  )}
                  {row.type === 'sig' && (
                    <div style={{ paddingLeft: 18 }}>
                      <span className="dim">Signature: </span>
                      <span className="blue">{row.text}</span>
                    </div>
                  )}
                  {row.type === 'ref' && (
                    <div>
                      <span className="dim">{row.n} </span>
                      <span className="path">{row.path}</span>
                      {row.lines && <span className="dim">:{row.lines}</span>}
                      {row.meta && <span className="dim">  {row.meta}</span>}
                    </div>
                  )}
                  {row.type === 'sum' && (
                    <div className="dim">{row.text}</div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
      <div className="tab-desc">{tab.desc}</div>
    </div>
  );
}
