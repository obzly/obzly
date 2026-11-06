import type { CheckView, PromQLSeries } from '../../../entity/synthetics';

/**
 * A monitor's colour: green up, amber warn, red down, blue off (disabled),
 * grey none (no results yet), half-filled amber impacted (failing while a
 * parent is down; its alerts are muted), slate maintenance (failing during
 * a maintenance window; alerts muted, uptime leaves it out).
 */
export type MonitorStatus = 'up' | 'warn' | 'down' | 'impacted' | 'maintenance' | 'off' | 'none';

export interface LocationState {
  location: string;
  up: boolean;
  warn: boolean;
}

export interface MonitorState {
  status: MonitorStatus;
  locations: LocationState[];
}

/** Red first, so problems lead a list sorted by status. */
export const STATUS_ORDER: Record<MonitorStatus, number> = {
  down: 0,
  impacted: 1,
  warn: 2,
  maintenance: 3,
  up: 4,
  none: 5,
  off: 6,
};

export const STATUS_LABEL: Record<MonitorStatus, string> = {
  up: 'Up',
  warn: 'Degraded',
  down: 'Down',
  impacted: 'Impacted',
  maintenance: 'Maintenance',
  off: 'Disabled',
  none: 'No data',
};

export const STATUS_COLOR: Record<MonitorStatus, string> = {
  up: '#10b981',
  warn: '#f59e0b',
  down: '#ef4444',
  impacted: '#f59e0b',
  maintenance: '#64748b',
  off: '#3b82f6',
  none: '#9ca3af',
};

/** How many failing locations make a monitor red: its down_locations, else half of them (at least one). */
export const downThreshold = (check: CheckView, locations: number): number =>
  check.downLocations > 0 ? check.downLocations : Math.max(1, Math.ceil(locations / 2));

/**
 * The monitor's colour from the latest run per location: red once at least
 * downThreshold locations fail, amber when fewer fail or a run passed with a
 * warning, green when all passed. up and warn are last_over_time over the
 * monitor's status window, so a location without a recent run is left out;
 * several series for one location (older probes split them) count as failed
 * if any failed and warned if any warned.
 */
export const monitorState = (
  check: CheckView,
  up: PromQLSeries[],
  warn: PromQLSeries[],
): MonitorState => {
  if (!check.enabled) return { status: 'off', locations: [] };
  const latest = (series: PromQLSeries[], pick: (a: number, b: number) => number) => {
    const values = new Map<string, number>();
    for (const item of series) {
      if (item.labels.check_id !== check.id) continue;
      const point = item.points[item.points.length - 1];
      if (!point) continue;
      const location = item.labels.location ?? '';
      const current = values.get(location);
      values.set(location, current === undefined ? point.value : pick(current, point.value));
    }
    return values;
  };
  const ups = latest(up, Math.min);
  const warns = latest(warn, Math.max);
  const locations = [...ups.keys()]
    .filter((location) => check.locations.length === 0 || check.locations.includes(location))
    .sort()
    .map((location) => ({
      location,
      up: (ups.get(location) ?? 0) >= 1,
      warn: (warns.get(location) ?? 0) >= 1,
    }));
  if (locations.length === 0) return { status: 'none', locations };
  const failing = locations.filter((location) => !location.up).length;
  let status: MonitorStatus = 'up';
  if (failing >= downThreshold(check, locations.length)) status = 'down';
  else if (failing > 0 || locations.some((location) => location.warn)) status = 'warn';
  return { status, locations };
};

/**
 * The monitors failing (down or degraded) while a parent is down, each with
 * those parents. Parents are judged on their own results, so a chain marks
 * every level below the first down monitor.
 */
export const impactedBy = (
  checks: CheckView[],
  statusOf: (check: CheckView) => MonitorStatus,
): Map<string, CheckView[]> => {
  const byId = new Map(checks.map((check) => [check.id, check]));
  const impacted = new Map<string, CheckView[]>();
  for (const check of checks) {
    const status = statusOf(check);
    if (status !== 'down' && status !== 'warn') continue;
    const down = (check.parentIds ?? [])
      .map((id) => byId.get(id))
      .filter((parent): parent is CheckView => !!parent && statusOf(parent) === 'down');
    if (down.length > 0) impacted.set(check.id, down);
  }
  return impacted;
};

export const locationStatus = (state: LocationState): MonitorStatus => {
  if (!state.up) return 'down';
  return state.warn ? 'warn' : 'up';
};

/** Groups monitors by where their results are, so each destination is queried once. */
export const byDestination = (checks: CheckView[]): Map<string, CheckView[]> => {
  const groups = new Map<string, CheckView[]>();
  for (const check of checks) {
    if (!check.destination.siteId || !check.destination.projectId) continue;
    const key = `${check.destination.siteId}/${check.destination.projectId}`;
    const group = groups.get(key);
    if (group) group.push(check);
    else groups.set(key, [check]);
  }
  return groups;
};

/** localeCompare's order without building a collator per comparison. */
export const byText = new Intl.Collator().compare;
