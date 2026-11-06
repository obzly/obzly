import { useEffect, useMemo, useState } from 'react';

import {
  type CheckView,
  type MaintenanceOccurrence,
  syntheticsApi,
} from '../../../entity/synthetics';
import { intervalsFor, uptimeExcluding } from './maintenance';
import { indexBy } from './queries';
import { stripWindow } from './strip';
import { RANGE_SECONDS, type RangeKey } from './useMonitorData';

const UPTIME_POINTS = 1440;

/**
 * A monitor's uptime per location over the range, leaving maintenance out;
 * undefined when no maintenance falls in the range (the plain uptime holds).
 */
export const useLocationUptime = (
  check: CheckView,
  range: RangeKey,
  maintenance: MaintenanceOccurrence[],
): Map<string, number> | undefined => {
  const [uptime, setUptime] = useState<Map<string, number>>();
  const occurrences = useMemo(
    () => maintenance.filter((item) => item.monitorIds.includes(check.id)),
    [maintenance, check.id],
  );

  useEffect(() => {
    const seconds = RANGE_SECONDS[range];
    const { from, to } = stripWindow(seconds);
    const intervals = intervalsFor(check.id, occurrences, from, to);
    setUptime(undefined);
    if (intervals.length === 0) return;
    const controller = new AbortController();
    const step = Math.max(60, Math.round(seconds / UPTIME_POINTS));
    const { siteId, projectId } = check.destination;
    const ranged = (query: string) =>
      syntheticsApi.promql(
        siteId,
        projectId,
        {
          query,
          // The point at t covers (t - step, t]: none reaches before the range.
          start: new Date(from + step * 1000).toISOString(),
          end: new Date(to).toISOString(),
          stepSeconds: step,
        },
        controller.signal,
      );
    const selector = `check_id="${check.id}"`;
    Promise.all([
      ranged(`sum by (location) (sum_over_time(synthetics_up{${selector}}[${step}s]))`),
      ranged(`sum by (location) (count_over_time(synthetics_up{${selector}}[${step}s]))`),
    ])
      .then(([passed, runs]) => {
        const byLocation = new Map<string, number>();
        const passes = indexBy(passed.series, 'location');
        for (const series of runs.series) {
          const location = series.labels.location;
          const value = uptimeExcluding(
            passes.get(location)?.[0]?.points ?? [],
            series.points,
            step,
            intervals,
          );
          if (value !== undefined) byLocation.set(location, value);
        }
        setUptime(byLocation);
      })
      .catch(() => {
        // The plain uptime stays shown.
      });
    return () => controller.abort();
  }, [check, range, occurrences]);

  return uptime;
};
