import { useCallback, useEffect, useRef } from 'react';

// setTimeout-like scheduling driven by requestAnimationFrame.
//
// The demos are chains of hundreds of tiny timeouts; browsers clamp
// background-tab timers to ~1/sec (later 1/min), so a demo left in a
// background tab comes back looking frozen and crawls one keystroke per
// second. rAF instead PAUSES while hidden and catches up on return —
// each overdue callback fires on its own frame, so the demo fast-forwards
// to where it should be in under a couple of seconds.
export function useFrameTimeouts() {
  const pendingRef = useRef([]);

  useEffect(() => {
    let raf = 0;
    let mounted = true;
    const tick = () => {
      if (!mounted) return;
      const now = performance.now();
      const due = [];
      pendingRef.current = pendingRef.current.filter(p => (p.at <= now ? (due.push(p), false) : true));
      due.forEach(p => p.fn());
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => { mounted = false; cancelAnimationFrame(raf); };
  }, []);

  const T = useCallback((fn, ms) => {
    pendingRef.current.push({ fn, at: performance.now() + ms });
  }, []);
  const clearAll = useCallback(() => { pendingRef.current = []; }, []);
  return { T, clearAll };
}
