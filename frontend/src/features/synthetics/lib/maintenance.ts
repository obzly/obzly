import type {
  MaintenanceOccurrence,
  MaintenanceSchedule,
  MaintenanceWindow,
  PromQLPoint,
} from '../../../entity/synthetics';
import type { MonitorStatus } from './status';
import type { SlotStatus } from './strip';
import type { MonitorMaintenance } from './useMonitorData';

/** A period in epoch milliseconds. */
export interface Interval {
  start: number;
  end: number;
}

/** How long before an occurrence the monitors list shows its wrench. */
export const SOON_MS = 60 * 60 * 1000;

/** Slate: maintenance is neither good nor bad. */
export const MAINTENANCE_COLOR = '#64748b';
export const MAINTENANCE_HATCH =
  'repeating-linear-gradient(135deg, #64748b 0 3px, #cbd5e1 3px 6px)';

/** Occurrences per monitor, built once instead of scanning every occurrence for every monitor. */
export const occurrencesByMonitor = (
  occurrences: MaintenanceOccurrence[],
): Map<string, MaintenanceOccurrence[]> => {
  const index = new Map<string, MaintenanceOccurrence[]>();
  for (const item of occurrences) {
    for (const id of item.monitorIds) {
      const list = index.get(id);
      if (list) list.push(item);
      else index.set(id, [item]);
    }
  }
  return index;
};

/** Changes whenever anything shown about the occurrences does, without serialising them whole. */
export const occurrencesKey = (occurrences: MaintenanceOccurrence[]): string =>
  occurrences
    .map(
      (item) =>
        `${item.windowId}|${item.start}|${item.end}|${item.name}|${item.reason}|${item.monitorIds.join(',')}`,
    )
    .join('\n');

/** The occurrences covering checkId that overlap [from, to). */
export const intervalsFor = (
  checkId: string,
  occurrences: MaintenanceOccurrence[],
  from: number,
  to: number,
): Interval[] =>
  occurrences
    .filter((item) => item.monitorIds.includes(checkId))
    .map((item) => ({ start: Date.parse(item.start), end: Date.parse(item.end) }))
    .filter((item) => item.end > from && item.start < to);

/** The occurrence checkId is in at `now`, if any. */
export const activeFor = (
  checkId: string,
  occurrences: MaintenanceOccurrence[],
  now: number,
): MaintenanceOccurrence | undefined =>
  occurrences.find(
    (item) =>
      item.monitorIds.includes(checkId) &&
      Date.parse(item.start) <= now &&
      Date.parse(item.end) > now,
  );

/** The occurrence checkId enters within SOON_MS of `now`, if any. */
export const upcomingFor = (
  checkId: string,
  occurrences: MaintenanceOccurrence[],
  now: number,
): MaintenanceOccurrence | undefined =>
  occurrences.find(
    (item) =>
      item.monitorIds.includes(checkId) &&
      Date.parse(item.start) > now &&
      Date.parse(item.start) <= now + SOON_MS,
  );

/**
 * A monitor in maintenance shows Maintenance instead of red or amber; green
 * stays green (a change that finished early shows), and disabled or no data
 * stay as they are.
 */
export const withMaintenance = (status: MonitorStatus, inMaintenance: boolean): MonitorStatus =>
  inMaintenance && (status === 'down' || status === 'warn' || status === 'impacted')
    ? 'maintenance'
    : status;

/**
 * Marks the slots in maintenance: those whose middle falls in an occurrence,
 * or that hold a whole (short) occurrence.
 */
export const markMaintenance = (
  slots: SlotStatus[],
  from: number,
  stepSeconds: number,
  intervals: Interval[],
): SlotStatus[] => {
  if (intervals.length === 0) return slots;
  const step = stepSeconds * 1000;
  return slots.map((slot, index) => {
    const start = from + index * step;
    const end = start + step;
    const middle = start + step / 2;
    const covered = intervals.some(
      (item) =>
        (item.start <= middle && item.end > middle) || (item.start >= start && item.end <= end),
    );
    return covered ? 'maintenance' : slot;
  });
};

/**
 * Uptime leaving maintenance out: passed and runs are per-step sums
 * (sum_over_time / count_over_time over [step]) at the end of each step, and
 * a step whose middle falls in an occurrence doesn't count. Undefined
 * without runs.
 */
export const uptimeExcluding = (
  passed: PromQLPoint[],
  runs: PromQLPoint[],
  stepSeconds: number,
  intervals: Interval[],
): number | undefined => {
  const passedAt = new Map(passed.map((point) => [Date.parse(point.timestamp), point.value]));
  const step = stepSeconds * 1000;
  let total = 0;
  let good = 0;
  for (const point of runs) {
    const at = Date.parse(point.timestamp);
    const middle = at - step / 2;
    if (intervals.some((item) => item.start <= middle && item.end > middle)) continue;
    total += point.value;
    good += passedAt.get(at) ?? 0;
  }
  return total > 0 ? good / total : undefined;
};

/** Total time of intervals within [from, to), overlaps counted once. */
export const coveredMs = (intervals: Interval[], from: number, to: number): number => {
  const clipped = intervals
    .map((item) => ({ start: Math.max(item.start, from), end: Math.min(item.end, to) }))
    .filter((item) => item.end > item.start)
    .sort((a, b) => a.start - b.start);
  let total = 0;
  let reach = -Infinity;
  for (const item of clipped) {
    const start = Math.max(item.start, reach);
    if (item.end > start) total += item.end - start;
    reach = Math.max(reach, item.end);
  }
  return total;
};

/** Runs of consecutive maintenance slots, for the hatched overlay. */
export const maintenanceRuns = (slots: SlotStatus[]): { start: number; length: number }[] => {
  const runs: { start: number; length: number }[] = [];
  slots.forEach((slot, index) => {
    if (slot !== 'maintenance') return;
    const last = runs[runs.length - 1];
    if (last && last.start + last.length === index) last.length += 1;
    else runs.push({ start: index, length: 1 });
  });
  return runs;
};

const WEEKDAY_LABEL: Record<string, string> = {
  mon: 'Mon',
  tue: 'Tue',
  wed: 'Wed',
  thu: 'Thu',
  fri: 'Fri',
  sat: 'Sat',
  sun: 'Sun',
};

export const WEEKDAYS = Object.keys(WEEKDAY_LABEL) as (keyof typeof WEEKDAY_LABEL)[];

export const weekdayLabel = (day: string) => WEEKDAY_LABEL[day] ?? day;

const ORDINAL = ['', '1st', '2nd', '3rd', '4th'];

/** "1 h", "20 min", "1 h 30 min". */
export const durationText = (minutes: number): string => {
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (hours === 0) return `${rest} min`;
  return rest ? `${hours} h ${rest} min` : `${hours} h`;
};

const oneOffFormat = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});

/** "Weekly · Tue, Fri 01:00 · 1 h · UTC", "One-off · 2 Nov, 22:00 to 3 Nov, 01:30". */
export const scheduleText = (schedule: MaintenanceSchedule): string => {
  if (schedule.kind === 'once') {
    if (!schedule.startsAt || !schedule.endsAt) return 'One-off';
    return `One-off · ${oneOffFormat.format(Date.parse(schedule.startsAt))} to ${oneOffFormat.format(Date.parse(schedule.endsAt))}`;
  }
  const at = schedule.startTime ?? '';
  const duration = durationText(schedule.durationMinutes ?? 0);
  const zone = schedule.timeZone ?? 'UTC';
  let repeat = 'Daily';
  if (schedule.kind === 'weekly') {
    repeat = `Weekly · ${(schedule.weekdays ?? []).map(weekdayLabel).join(', ')}`;
  } else if (schedule.kind === 'monthly_day') {
    repeat = `Monthly · day ${schedule.dayOfMonth}`;
  } else if (schedule.kind === 'monthly_weekday') {
    const week = schedule.weekOfMonth === -1 ? 'last' : ORDINAL[schedule.weekOfMonth ?? 0];
    repeat = `Monthly · ${week} ${weekdayLabel(schedule.weekday ?? '')}`;
  }
  const until = schedule.endDate ? ` · until ${schedule.endDate}` : '';
  return `${repeat} ${at} · ${duration} · ${zone}${until}`;
};

/** What a window covers, e.g. "Shop/Checkout (4), Payments gateway TLS". */
export const coversText = (
  window: Pick<MaintenanceWindow, 'scope'>,
  names: Map<string, string>,
  folderCount: (folder: string) => number,
): string => {
  if (window.scope.all) return 'All monitors';
  const parts = [
    ...window.scope.folders.map((folder) => `${folder} (${folderCount(folder)})`),
    ...window.scope.monitorIds.map((id) => names.get(id) ?? 'a deleted monitor'),
  ];
  return parts.join(', ') || 'Nothing';
};

/** Local midnight `days` days after the one before `at`. */
export const dayStart = (at: number, days = 0): number => {
  const date = new Date(at);
  return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days).getTime();
};

/**
 * Which of `days` local days from `from` each window has maintenance on, for
 * the calendar strip.
 */
export const calendarDays = (
  occurrences: MaintenanceOccurrence[],
  from: number,
  days: number,
): Map<string, boolean[]> => {
  const result = new Map<string, boolean[]>();
  for (const item of occurrences) {
    const cells = result.get(item.windowId) ?? Array<boolean>(days).fill(false);
    const start = Date.parse(item.start);
    const end = Date.parse(item.end);
    for (let day = 0; day < days; day++) {
      if (start < dayStart(from, day + 1) && end > dayStart(from, day)) cells[day] = true;
    }
    result.set(item.windowId, cells);
  }
  return result;
};

/**
 * The maintenance that matters for a monitor now: the one it is in, else the
 * one it enters within the hour, else the latest one in the range.
 */
export const relevantOccurrence = (
  checkId: string,
  occurrences: MaintenanceOccurrence[],
  now: number,
  from: number,
): MaintenanceOccurrence | undefined => {
  const mine = occurrences.filter((item) => item.monitorIds.includes(checkId));
  const start = (item: MaintenanceOccurrence) => Date.parse(item.start);
  const end = (item: MaintenanceOccurrence) => Date.parse(item.end);
  return (
    mine.find((item) => start(item) <= now && end(item) > now) ??
    mine.find((item) => start(item) > now && start(item) <= now + SOON_MS) ??
    mine.filter((item) => end(item) <= now && end(item) > from).sort((a, b) => end(b) - end(a))[0]
  );
};

const clockTime = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

/** What a monitor's maintenance means now, e.g. "In maintenance until 02:00: Weekly DB patching". */
export const maintenanceTitle = (maintenance?: MonitorMaintenance): string | undefined => {
  if (maintenance?.active) {
    return `In maintenance until ${clockTime.format(Date.parse(maintenance.active.end))}: ${maintenance.active.name}`;
  }
  if (maintenance?.upcoming) {
    return `Maintenance from ${clockTime.format(Date.parse(maintenance.upcoming.start))}: ${maintenance.upcoming.name}`;
  }
  return undefined;
};
