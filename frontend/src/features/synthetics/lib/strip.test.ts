import { describe, expect, it } from 'vitest';

import type { PromQLPoint } from '../../../entity/synthetics';
import { STRIP_SLOTS, locationStrip, monitorStrip, perSlot, slotOf, stripWindow } from './strip';

const from = Date.parse('2026-10-25T08:00:00Z');
const step = 75;
// Range points as the engine returns them: at from + k * step, each covering
// (t - step, t]. Point k belongs to slot k - 1.
const at = (k: number) => new Date(from + k * step * 1000).toISOString();
const points = (values: [number, number][]): PromQLPoint[] =>
  values.map(([k, value]) => ({ timestamp: at(k), value }));

describe('stripWindow', () => {
  it('ends on a whole step after now so slot edges hold between refreshes', () => {
    const now = Date.parse('2026-10-25T08:10:20Z');
    const window = stripWindow(86400, now);
    expect(window.step).toBe(1800);
    expect(window.to).toBe(Date.parse('2026-10-25T08:30:00Z'));
    expect(window.to - window.from).toBe(STRIP_SLOTS * 1800 * 1000);
    expect(stripWindow(86400, now + 60_000)).toEqual(window);
  });

  it('keeps 75 s slots for an hour', () => {
    const window = stripWindow(3600, from + 10_000);
    expect(window.step).toBe(75);
    expect(window.to % (75 * 1000)).toBe(0);
  });
});

describe('slotOf', () => {
  it('puts the point at from before the strip and point k in slot k - 1', () => {
    expect(slotOf(at(0), from, step)).toBe(-1);
    expect(slotOf(at(1), from, step)).toBe(0);
    expect(slotOf(at(STRIP_SLOTS), from, step)).toBe(STRIP_SLOTS - 1);
    expect(slotOf(at(STRIP_SLOTS + 1), from, step)).toBe(STRIP_SLOTS);
  });

  it('drops points outside the strip instead of piling them into the last slot', () => {
    const values = perSlot(
      points([
        [0, 5],
        [1, 1],
        [STRIP_SLOTS, 2],
        [STRIP_SLOTS + 1, 9],
      ]),
      from,
      step,
    );
    expect(values[0]).toBe(1);
    expect(values[STRIP_SLOTS - 1]).toBe(2);
    expect(values).toHaveLength(STRIP_SLOTS);
  });
});

describe('locationStrip', () => {
  it('is red where a run failed, amber where one warned, empty without runs', () => {
    const slots = locationStrip(
      points([
        [0, 0],
        [1, 1],
        [2, 0],
        [3, 1],
      ]),
      points([[3, 1]]),
      from,
      step,
    );
    expect(slots.slice(0, 4)).toEqual(['up', 'down', 'warn', 'none']);
  });

  it("carries a run's state until the next run is due", () => {
    // A 3-minute monitor in the 1 h view: a run every 2.4 slots.
    const runs = Array.from({ length: 15 }, (_, i): [number, number] => [
      Math.floor((i * 180 + 10) / step) + 1,
      1,
    ]);
    expect(
      locationStrip(points(runs), [], from, step).filter((slot) => slot === 'none').length,
    ).toBeGreaterThan(20);
    const slots = locationStrip(points(runs), [], from, step, 180);
    const lastRun = runs[runs.length - 1][0] - 1;
    expect(slots.slice(0, lastRun + 1).every((slot) => slot === 'up')).toBe(true);
    // Once the next run is overdue (interval plus slack), missing runs show again.
    expect(slots[lastRun + 3]).toBe('up');
    expect(slots.slice(lastRun + 4).every((slot) => slot === 'none')).toBe(true);
  });

  it('keeps a failure red until the next run, not longer', () => {
    const slots = locationStrip(
      points([
        [1, 0],
        [3, 1],
      ]),
      [],
      from,
      step,
      180,
    );
    expect(slots.slice(0, 3)).toEqual(['down', 'down', 'up']);
  });
});

describe('monitorStrip', () => {
  const half = (locations: number) => Math.max(1, Math.ceil(locations / 2));

  it('is red only once failing locations reach the threshold, amber below it', () => {
    const slots = monitorStrip(
      points([
        [1, 0],
        [2, 1],
        [3, 2],
        [4, 0],
      ]),
      points([
        [1, 4],
        [2, 4],
        [3, 4],
        [4, 4],
      ]),
      points([[4, 1]]),
      from,
      step,
      half,
    );
    expect(slots.slice(0, 5)).toEqual(['up', 'warn', 'down', 'warn', 'none']);
  });

  it('follows down_locations', () => {
    const slots = monitorStrip(points([[1, 1]]), points([[1, 3]]), [], from, step, () => 1);
    expect(slots[0]).toBe('down');
  });
});
