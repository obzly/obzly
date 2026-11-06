import { useEffect, useRef } from 'react';

interface VisibilitySource {
  hidden: boolean;
  addEventListener: (type: 'visibilitychange', listener: () => void) => void;
  removeEventListener: (type: 'visibilitychange', listener: () => void) => void;
}

/** Skips ticks while the tab is hidden and catches up once when shown; returns a cleanup. */
export const visibleInterval = (
  tick: () => void,
  ms: number,
  source: VisibilitySource = document,
): (() => void) => {
  let missed = false;
  const timer = setInterval(() => {
    if (source.hidden) missed = true;
    else tick();
  }, ms);
  const onVisibility = () => {
    if (!source.hidden && missed) {
      missed = false;
      tick();
    }
  };
  source.addEventListener('visibilitychange', onVisibility);
  return () => {
    clearInterval(timer);
    source.removeEventListener('visibilitychange', onVisibility);
  };
};

/** setInterval that pauses in hidden tabs; always calls the latest callback. */
export const useVisibleInterval = (callback: () => void, ms: number, enabled = true): void => {
  const latest = useRef(callback);
  useEffect(() => {
    latest.current = callback;
  });
  useEffect(() => {
    if (!enabled) return;
    return visibleInterval(() => latest.current(), ms);
  }, [ms, enabled]);
};
