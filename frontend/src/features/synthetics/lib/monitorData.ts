import type { MaintenanceOccurrence } from '../../../entity/synthetics';
import type { MonitorData, MonitorDataResult } from './useMonitorData';

const sameOccurrence = (a?: MaintenanceOccurrence, b?: MaintenanceOccurrence) =>
  a === b ||
  (!!a &&
    !!b &&
    a.windowId === b.windowId &&
    a.start === b.start &&
    a.end === b.end &&
    a.name === b.name);

const sameList = <T>(a: T[], b: T[], same: (x: T, y: T) => boolean) =>
  a.length === b.length && a.every((item, index) => same(item, b[index]));

/** Whether two results for one monitor would render the same. */
export const sameMonitorData = (a: MonitorData, b: MonitorData): boolean =>
  a.state.status === b.state.status &&
  sameList(
    a.state.locations,
    b.state.locations,
    (x, y) => x.location === y.location && x.up === y.up && x.warn === y.warn,
  ) &&
  a.uptime === b.uptime &&
  a.p95Ms === b.p95Ms &&
  a.certDays === b.certDays &&
  sameList(a.strip, b.strip, (x, y) => x === y) &&
  sameList(a.impactedBy ?? [], b.impactedBy ?? [], (x, y) => x.id === y.id) &&
  sameOccurrence(a.maintenance?.active, b.maintenance?.active) &&
  sameOccurrence(a.maintenance?.upcoming, b.maintenance?.upcoming) &&
  a.maintenance?.excludedMs === b.maintenance?.excludedMs;

/**
 * next with every unchanged monitor's object taken from previous, or previous
 * itself when nothing changed, so a refresh with the same numbers renders
 * nothing.
 */
export const reuseUnchanged = (
  previous: MonitorDataResult,
  next: MonitorDataResult,
): MonitorDataResult => {
  let changed =
    previous.loading !== next.loading ||
    previous.from !== next.from ||
    previous.to !== next.to ||
    previous.stepSeconds !== next.stepSeconds ||
    previous.data.size !== next.data.size ||
    !sameList(previous.errors, next.errors, (x, y) => x === y);
  const data = new Map<string, MonitorData>();
  for (const [id, item] of next.data) {
    const old = previous.data.get(id);
    if (old && sameMonitorData(old, item)) data.set(id, old);
    else {
      data.set(id, item);
      changed = true;
    }
  }
  return changed ? { ...next, data } : previous;
};
