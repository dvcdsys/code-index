// The "one server, whole team" diagram — used on the landing (Features
// flagship card) and in the docs Multi-user section. Brand style: ink
// outlines, paper fills, mono labels, slight sticker-like rotations.
export function TeamDiagram() {
  const ink = 'var(--ink)';
  const paper = 'var(--paper)';
  const mono = 'var(--font-mono)';
  const box = { fill: paper, stroke: ink, strokeWidth: 2.5 };
  const label = { fontFamily: mono, fontSize: 12, fill: ink };
  const small = { fontFamily: mono, fontSize: 10.5, fill: 'var(--ink-mute)' };
  return (
    <svg viewBox="0 0 760 340" role="img" aria-label="Many users and agents talk to one cix server with roles, view-groups and API keys; the server indexes repositories and reindexes on GitHub push" style={{ width: '100%', height: 'auto', display: 'block' }}>
      <defs>
        <marker id="td-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill={ink} />
        </marker>
      </defs>

      {/* left: clients */}
      <g transform="rotate(-2 100 55)">
        <rect x="14" y="24" width="178" height="62" rx="12" fill="var(--code-bg)" stroke={ink} strokeWidth="2.5" />
        <text x="30" y="50" fontFamily={mono} fontSize="12" fill="var(--code-fg)"><tspan fill="var(--red)">$</tspan> cix search "…"</text>
        <text x="30" y="72" fontFamily={mono} fontSize="10.5" fill="var(--code-mute)">dev · CLI</text>
      </g>
      <g transform="rotate(1.5 100 165)">
        <rect x="14" y="134" width="178" height="62" rx="12" {...box} />
        <text x="30" y="160" style={label}>✳ agent</text>
        <text x="30" y="182" style={small}>Claude Code · MCP · any shell</text>
      </g>
      <g transform="rotate(-1.5 100 275)">
        <rect x="14" y="244" width="178" height="62" rx="12" {...box} />
        <text x="30" y="270" style={label}>◨ browser</text>
        <text x="30" y="292" style={small}>dashboard · admin</text>
      </g>

      {/* arrows clients → server */}
      <line x1="196" y1="55" x2="296" y2="130" stroke={ink} strokeWidth="2.5" markerEnd="url(#td-arrow)" />
      <line x1="196" y1="165" x2="296" y2="168" stroke={ink} strokeWidth="2.5" markerEnd="url(#td-arrow)" />
      <line x1="196" y1="275" x2="296" y2="206" stroke={ink} strokeWidth="2.5" markerEnd="url(#td-arrow)" />
      <text x="206" y="142" style={small}>Bearer cix_…</text>
      <text x="222" y="252" style={small}>session</text>

      {/* center: server */}
      <g>
        <rect x="302" y="72" width="196" height="216" rx="16" {...box} />
        <text x="322" y="102" fontFamily={mono} fontSize="15" fontWeight="700" fill={ink}>cix server</text>
        <text x="322" y="120" style={small}>:21847 · one for everyone</text>
        <rect x="320" y="136" width="160" height="34" rx="9" fill="var(--bg-2)" stroke={ink} strokeWidth="2" />
        <text x="334" y="158" style={label}>roles</text>
        <rect x="320" y="180" width="160" height="34" rx="9" fill="var(--bg-2)" stroke={ink} strokeWidth="2" />
        <text x="334" y="202" style={label}>view-groups</text>
        <rect x="320" y="224" width="160" height="34" rx="9" fill="var(--bg-2)" stroke={ink} strokeWidth="2" />
        <text x="334" y="246" style={label}>API keys</text>
      </g>

      {/* github webhook */}
      <g transform="rotate(2 640 40)">
        <rect x="566" y="16" width="150" height="48" rx="24" {...box} />
        <text x="592" y="45" style={label}>⊙ GitHub</text>
      </g>
      <path d="M 610 66 C 580 96, 540 92, 502 108" fill="none" stroke={ink} strokeWidth="2.5" strokeDasharray="6 5" markerEnd="url(#td-arrow)" />
      <text x="608" y="106" style={small}>push → reindex</text>

      {/* server → repos */}
      <line x1="498" y1="200" x2="566" y2="200" stroke={ink} strokeWidth="2.5" markerEnd="url(#td-arrow)" />
      <text x="504" y="190" style={small}>search</text>

      {/* right: repo stack */}
      <g transform="rotate(1.5 650 210)">
        <rect x="600" y="140" width="140" height="52" rx="10" fill="var(--bg-2)" stroke={ink} strokeWidth="2" />
        <rect x="588" y="158" width="140" height="52" rx="10" fill="var(--bg-2)" stroke={ink} strokeWidth="2" />
        <rect x="576" y="176" width="150" height="52" rx="10" {...box} />
        <text x="590" y="199" style={label}>repo × N</text>
        <text x="590" y="217" style={small}>projects · workspaces</text>
      </g>
      <text x="576" y="262" style={small}>private by default —</text>
      <text x="576" y="278" style={small}>shared via view-groups</text>
    </svg>
  );
}
