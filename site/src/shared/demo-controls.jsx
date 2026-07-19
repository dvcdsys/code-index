// Play / stop for the animated demos. Stop skips straight to the finished
// state (no half-read output); play restarts the loop from the top.
export function DemoControls({ running, onPlay, onStop }) {
  return (
    <span className="demo-ctl">
      <button type="button" aria-label="Replay the demo" title="Replay" onClick={onPlay} disabled={running}>▶</button>
      <button type="button" aria-label="Skip to the final result" title="Skip to the final result" onClick={onStop} disabled={!running}>⏹</button>
    </span>
  );
}
