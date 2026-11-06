import type { PromQLPoint } from '../../../entity/synthetics';

/** Slots in an availability strip. */
export const STRIP_SLOTS = 48;

export type SlotStatus = 'up' | 'warn' | 'down' | 'maintenance' | 'none';

// A run stands for its monitor until the next one is due; slack covers a
// late run before a slot shows "no runs".
const RUN_SLACK_SECONDS = 60;

export interface StripWindow {
  from: number;
  to: number;
  /** Slot length in seconds. */
  step: number;
}

/**
 * The strip's time window for a range. `to` is rounded up to a whole step so
 * slot edges stay put between refreshes; the last slot is the one now is in.
 */
export const stripWindow = (seconds: number, now = Date.now()): StripWindow => {
  const step = Math.max(60, Math.round(seconds / STRIP_SLOTS));
  const to = Math.ceil(now / (step * 1000)) * step * 1000;
  return { from: to - STRIP_SLOTS * step * 1000, to, step };
};

/**
 * The slot of a range point: the point at t covers (t - step, t], so the one
 * at `from` is before the strip (-1) and the one at from + k * step is slot k - 1.
 */
export const slotOf = (timestamp: string, from: number, step: number): number =>
  Math.round((Date.parse(timestamp) - from) / (step * 1000)) - 1;

/** A range query's values per slot, undefined where it had no point. */
export const perSlot = (
  points: PromQLPoint[] | undefined,
  from: number,
  step: number,
): (number | undefined)[] => {
  const values: (number | undefined)[] = Array(STRIP_SLOTS).fill(undefined);
  for (const point of points ?? []) {
    const slot = slotOf(point.timestamp, from, step);
    if (slot >= 0 && slot < STRIP_SLOTS) values[slot] = point.value;
  }
  return values;
};

/**
 * With intervalSeconds longer than a slot, an empty slot takes the latest
 * run's state until the next is due, so a 3-minute monitor doesn't skip
 * every other 75-second slot.
 */
const carryForward = (slots: SlotStatus[], step: number, intervalSeconds: number) => {
  if (intervalSeconds <= step) return slots;
  let last = -1;
  for (let slot = 0; slot < slots.length; slot++) {
    if (slots[slot] !== 'none') last = slot;
    else if (last >= 0 && (slot - last) * step < intervalSeconds + RUN_SLACK_SECONDS) {
      slots[slot] = slots[last];
    }
  }
  return slots;
};

/**
 * One location's strip from min(synthetics_up) and max(synthetics_warn) per
 * step: red if any run failed, amber if any warned, green if all passed,
 * empty without runs.
 */
export const locationStrip = (
  up: PromQLPoint[] | undefined,
  warn: PromQLPoint[] | undefined,
  from: number,
  step: number,
  intervalSeconds = 0,
): SlotStatus[] => {
  const warns = perSlot(warn, from, step);
  const slots = perSlot(up, from, step).map((value, slot): SlotStatus => {
    if (value === undefined) return 'none';
    if (value < 1) return 'down';
    return (warns[slot] ?? 0) >= 1 ? 'warn' : 'up';
  });
  return carryForward(slots, step, intervalSeconds);
};

/**
 * A monitor's strip from the failing and reporting locations per step, by
 * the status rule: red once failing locations reach the threshold, amber when
 * fewer fail or a run warned, green when all passed, empty without runs.
 */
export const monitorStrip = (
  failing: PromQLPoint[] | undefined,
  locations: PromQLPoint[] | undefined,
  warn: PromQLPoint[] | undefined,
  from: number,
  step: number,
  threshold: (locations: number) => number,
  intervalSeconds = 0,
): SlotStatus[] => {
  const fails = perSlot(failing, from, step);
  const warns = perSlot(warn, from, step);
  const slots = perSlot(locations, from, step).map((count, slot): SlotStatus => {
    if (!count) return 'none';
    const failed = fails[slot] ?? 0;
    if (failed >= threshold(count)) return 'down';
    return failed > 0 || (warns[slot] ?? 0) >= 1 ? 'warn' : 'up';
  });
  return carryForward(slots, step, intervalSeconds);
};
