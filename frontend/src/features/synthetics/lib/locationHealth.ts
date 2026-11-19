import { withMaintenance } from './maintenance';
import { type LocationState, type MonitorStatus, locationStatus } from './status';

/** A monitor's latest result per location, and whether it is in maintenance now. */
export interface MonitorLocations {
  id: string;
  locations: LocationState[];
  inMaintenance: boolean;
}

export interface LocationSummary {
  location: string;
  /** Monitors with a result from this location. */
  total: number;
  counts: Record<'up' | 'warn' | 'down' | 'maintenance', number>;
}

/** A monitor's status at one location, with maintenance muting failures. */
export const statusAt = (state: LocationState, inMaintenance: boolean): MonitorStatus =>
  withMaintenance(locationStatus(state), inMaintenance);

/** One row per location: how many monitors pass, warn or fail from it. */
export const summarizeLocations = (
  locations: string[],
  monitors: MonitorLocations[],
): LocationSummary[] =>
  locations.map((location) => {
    const counts = { up: 0, warn: 0, down: 0, maintenance: 0 };
    for (const monitor of monitors) {
      const state = monitor.locations.find((s) => s.location === location);
      if (!state) continue;
      const status = statusAt(state, monitor.inMaintenance);
      if (status === 'up' || status === 'warn' || status === 'down' || status === 'maintenance') {
        counts[status]++;
      }
    }
    return {
      location,
      total: counts.up + counts.warn + counts.down + counts.maintenance,
      counts,
    };
  });

/** Whether any location reports the monitor failing or degraded (maintenance doesn't count). */
export const failsSomewhere = (monitor: MonitorLocations): boolean =>
  monitor.locations.some((state) => {
    const status = statusAt(state, monitor.inMaintenance);
    return status === 'down' || status === 'warn';
  });
