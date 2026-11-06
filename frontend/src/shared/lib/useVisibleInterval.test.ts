import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { visibleInterval } from './useVisibleInterval';

const fakeDocument = () => {
  const listeners = new Set<() => void>();
  return {
    hidden: false,
    addEventListener: (_: 'visibilitychange', listener: () => void) => listeners.add(listener),
    removeEventListener: (_: 'visibilitychange', listener: () => void) =>
      listeners.delete(listener),
    fire: () => listeners.forEach((listener) => listener()),
    listeners,
  };
};

describe('visibleInterval', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('Test_VisibleInterval_HiddenTab_SkipsTicksAndCatchesUpOnShow', () => {
    const doc = fakeDocument();
    const tick = vi.fn();
    const stop = visibleInterval(tick, 1000, doc);

    vi.advanceTimersByTime(2000);
    expect(tick).toHaveBeenCalledTimes(2);

    doc.hidden = true;
    vi.advanceTimersByTime(5000);
    expect(tick).toHaveBeenCalledTimes(2);

    doc.hidden = false;
    doc.fire();
    expect(tick).toHaveBeenCalledTimes(3);

    stop();
    vi.advanceTimersByTime(5000);
    expect(tick).toHaveBeenCalledTimes(3);
    expect(doc.listeners.size).toBe(0);
  });

  it('Test_VisibleInterval_ShownWithoutMissedTick_DoesNotTickEarly', () => {
    const doc = fakeDocument();
    const tick = vi.fn();
    const stop = visibleInterval(tick, 1000, doc);
    doc.hidden = true;
    doc.fire();
    doc.hidden = false;
    doc.fire();
    expect(tick).not.toHaveBeenCalled();
    stop();
  });
});
