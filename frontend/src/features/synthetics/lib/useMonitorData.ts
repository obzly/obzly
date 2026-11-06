import { useEffect, useRef, useState } from 'react';

import {
  type CheckView,
  type MaintenanceOccurrence,
  type PromQLResponse,
  type PromQLSeries,
  syntheticsApi,
} from '../../../entity/synthetics';
import { queryInChunks } from './chunks';
import {
  type Interval,
  activeFor,
  coveredMs,
  intervalsFor,
  markMaintenance,
  occurrencesByMonitor,
  upcomingFor,
  uptimeExcluding,
  withMaintenance,
} from './maintenance';
import { reuseUnchanged } from './monitorData';
import {
  groupBy,
  idsMatcher,
  indexBy,
  latestBy,
  mergedQuantile,
  passRatio,
  quantileResolution,
  selector,
  statusWindow,
} from './queries';
import { rangeCache } from './rangeCache';
import {
  type MonitorState,
  byDestination,
  downThreshold,
  impactedBy,
  monitorState,
} from './status';
import { STRIP_SLOTS, type SlotStatus, monitorStrip, stripWindow } from './strip';

export type RangeKey = '1h' | '24h' | '7d';

export const RANGE_SECONDS: Record<RangeKey, number> = {
  '1h': 3600,
  '24h': 86400,
  '7d': 7 * 86400,
};

export { STRIP_SLOTS, type SlotStatus, locationStrip, monitorStrip, stripWindow } from './strip';

export interface MonitorData {
  state: MonitorState;
  /** Share of runs that passed over the range, 0..1. */
  uptime?: number;
  p95Ms?: number;
  certDays?: number;
  strip: SlotStatus[];
  /** Down parents that make this monitor Impacted. */
  impactedBy?: CheckView[];
  maintenance?: MonitorMaintenance;
}

/** A monitor's maintenance: now, within the hour, and the time it takes out of the range. */
export interface MonitorMaintenance {
  active?: MaintenanceOccurrence;
  upcoming?: MaintenanceOccurrence;
  excludedMs: number;
}

export interface MonitorDataResult {
  data: Map<string, MonitorData>;
  /** Destinations whose results couldn't be read ("site/project": reason). */
  errors: string[];
  loading: boolean;
  from: number;
  to: number;
  stepSeconds: number;
}

/** Values over the whole range, per monitor ID. */
interface RangeValues {
  uptime: Map<string, number | undefined>;
  p95: Map<string, number>;
  cert: Map<string, number>;
}

const NO_MAINTENANCE: MaintenanceOccurrence[] = [];
const NO_SERIES: PromQLSeries[] = [];
const EMPTY: PromQLResponse = { resultType: 'matrix', series: [] };
// Uptime that leaves maintenance out counts runs per step of range/1440, so
// the edges of a window are off by at most a minute in a day.
const UPTIME_POINTS = 1440;
const CERT_WINDOW = '2h';
const MAX_SERIES = 10000;

/**
 * Status, uptime, p95, certificate days and availability strips for every
 * monitor, queried once per destination. Maintenance turns red and amber
 * into Maintenance, hatches its slots and leaves its time out of uptime.
 * Status and strips load on every refresh; uptime, p95 and certificates at
 * most every 5 minutes. Nothing loads while disabled.
 */
export const useMonitorData = (
  checks: CheckView[],
  range: RangeKey,
  refresh: number,
  maintenance: MaintenanceOccurrence[] = NO_MAINTENANCE,
  enabled = true,
): MonitorDataResult => {
  const [result, setResult] = useState<MonitorDataResult>(() => ({
    data: new Map(),
    errors: [],
    loading: true,
    from: 0,
    to: 0,
    stepSeconds: 0,
  }));
  const [cache] = useState(() => rangeCache<RangeValues>());
  // What the shown result was loaded for: coming back to a view reloads
  // only when something changed.
  const loaded = useRef<{
    checks: CheckView[];
    range: RangeKey;
    refresh: number;
    maintenance: MaintenanceOccurrence[];
  }>(undefined);

  useEffect(() => {
    if (!enabled) return;
    const last = loaded.current;
    if (
      last?.checks === checks &&
      last.range === range &&
      last.refresh === refresh &&
      last.maintenance === maintenance
    ) {
      return;
    }
    const controller = new AbortController();
    // A new range shows the spinner; a refresh swaps the numbers in quietly.
    if (last?.range !== range) {
      setResult((previous) => (previous.loading ? previous : { ...previous, loading: true }));
    }
    const seconds = RANGE_SECONDS[range];
    const { from, to, step } = stripWindow(seconds);
    const uptimeStep = Math.max(60, Math.round(seconds / UPTIME_POINTS));

    const load = async () => {
      const data = new Map<string, MonitorData>();
      const errors: string[] = [];
      const byMonitor = occurrencesByMonitor(maintenance);
      const intervals = new Map<string, Interval[]>();
      for (const check of checks) {
        const found = intervalsFor(check.id, byMonitor.get(check.id) ?? NO_MAINTENANCE, from, to);
        if (found.length > 0) intervals.set(check.id, found);
      }
      for (const check of checks) {
        if (!check.enabled) {
          data.set(check.id, {
            state: monitorState(check, [], []),
            strip: Array(STRIP_SLOTS).fill('none'),
          });
        }
      }
      await Promise.all(
        [...byDestination(checks.filter((check) => check.enabled))].map(async ([key, group]) => {
          const { siteId, projectId } = group[0].destination;
          const query = queriesFor(siteId, projectId, controller.signal);
          const inMaintenance = group.filter((check) => intervals.has(check.id));
          const rangeKey = [
            range,
            key,
            group.map((check) => `${check.id}:${check.intervalSeconds}`).join(','),
            inMaintenance
              .map(
                (check) =>
                  `${check.id}:${(intervals.get(check.id) ?? []).map((item) => `${item.start}-${item.end}`).join(';')}`,
              )
              .join(','),
          ].join('|');
          try {
            const [latest, strips, values] = await Promise.all([
              latestStatus(group, query),
              queryInChunks(group, (ids) => {
                const only = idsMatcher(ids, group.length);
                const perLocation = `min by (check_id, location) (min_over_time(${selector('synthetics_up', only)}[${step}s]))`;
                return Promise.all([
                  query.ranged(`sum by (check_id) (${perLocation} == bool 0)`, from, to, step),
                  query.ranged(`count by (check_id) (${perLocation})`, from, to, step),
                  query.ranged(
                    `max by (check_id) (max_over_time(${selector('synthetics_warn', only)}[${step}s]))`,
                    from,
                    to,
                    step,
                  ),
                ]);
              }),
              cache.load(rangeKey, () =>
                rangeValues(group, inMaintenance, intervals, query, {
                  seconds,
                  from,
                  to,
                  uptimeStep,
                }),
              ),
            ]);
            const [failing, reporting, warned] = strips.map((item) =>
              indexBy(item.series, 'check_id'),
            );
            for (const check of group) {
              const points = (index: Map<string, PromQLSeries[]>) =>
                index.get(check.id)?.[0]?.points;
              data.set(check.id, {
                state: monitorState(
                  check,
                  latest.up.get(check.id) ?? NO_SERIES,
                  latest.warn.get(check.id) ?? NO_SERIES,
                ),
                uptime: values.uptime.get(check.id),
                p95Ms: scaled(values.p95.get(check.id), 1000),
                certDays: scaled(values.cert.get(check.id), 1 / 86400),
                strip: markMaintenance(
                  monitorStrip(
                    points(failing),
                    points(reporting),
                    points(warned),
                    from,
                    step,
                    (locations) => downThreshold(check, locations),
                    check.intervalSeconds,
                  ),
                  from,
                  step,
                  intervals.get(check.id) ?? [],
                ),
              });
            }
          } catch (error) {
            if (controller.signal.aborted) return;
            errors.push(`${key}: ${(error as Error).message}`);
            for (const check of group) {
              data.set(check.id, {
                state: monitorState(check, [], []),
                strip: Array(STRIP_SLOTS).fill('none'),
              });
            }
          }
        }),
      );
      const ownStatus = (check: CheckView) => data.get(check.id)?.state.status ?? 'none';
      for (const [id, parents] of impactedBy(checks, ownStatus)) {
        const item = data.get(id);
        if (item)
          data.set(id, {
            ...item,
            state: { ...item.state, status: 'impacted' },
            impactedBy: parents,
          });
      }
      // After Impacted, which judges parents on their own results: a
      // child of a parent in maintenance still shows Impacted.
      const now = Date.now();
      for (const check of checks) {
        const item = data.get(check.id);
        const own = byMonitor.get(check.id);
        if (!item || !own) continue;
        const active = activeFor(check.id, own, now);
        const upcoming = upcomingFor(check.id, own, now);
        if (!active && !upcoming && !intervals.has(check.id)) continue;
        data.set(check.id, {
          ...item,
          state: { ...item.state, status: withMaintenance(item.state.status, !!active) },
          maintenance: {
            active,
            upcoming,
            excludedMs: coveredMs(intervals.get(check.id) ?? [], from, to),
          },
        });
      }
      if (controller.signal.aborted) return;
      loaded.current = { checks, range, refresh, maintenance };
      setResult((previous) =>
        reuseUnchanged(previous, { data, errors, loading: false, from, to, stepSeconds: step }),
      );
    };
    void load();
    return () => controller.abort();
  }, [checks, range, refresh, maintenance, enabled, cache]);

  return result;
};

interface Query {
  instant: (query: string) => Promise<PromQLResponse>;
  ranged: (query: string, from: number, to: number, step: number) => Promise<PromQLResponse>;
}

const queriesFor = (siteId: string, projectId: string, signal: AbortSignal): Query => ({
  instant: (query) =>
    syntheticsApi.promql(
      siteId,
      projectId,
      { query, instant: true, maxSeries: MAX_SERIES },
      signal,
    ),
  ranged: (query, from, to, step) =>
    syntheticsApi.promql(
      siteId,
      projectId,
      {
        query,
        start: new Date(from).toISOString(),
        end: new Date(to).toISOString(),
        stepSeconds: step,
        maxSeries: MAX_SERIES,
      },
      signal,
    ),
});

/**
 * The latest run per monitor and location, each monitor over its own status
 * window, so a location that stopped reporting drops out instead of keeping
 * its last state for hours. One query pair per distinct window.
 */
const latestStatus = async (
  group: CheckView[],
  query: Query,
): Promise<{ up: Map<string, PromQLSeries[]>; warn: Map<string, PromQLSeries[]> }> => {
  const up: PromQLSeries[] = [];
  const warn: PromQLSeries[] = [];
  await Promise.all(
    [...groupBy(group, (check) => statusWindow(check.intervalSeconds))].map(
      async ([window, members]) => {
        const [ups, warns] = await queryInChunks(members, (ids) => {
          const only = idsMatcher(ids, group.length);
          return Promise.all([
            query.instant(
              `min by (check_id, location) (last_over_time(${selector('synthetics_up', only)}[${window}s]))`,
            ),
            query.instant(
              `max by (check_id, location) (last_over_time(${selector('synthetics_warn', only)}[${window}s]))`,
            ),
          ]);
        });
        // A query without the ID matcher answers for every monitor; keep
        // only this window's.
        const ids = new Set(members.map((check) => check.id));
        up.push(...(ups?.series ?? []).filter((item) => ids.has(item.labels.check_id)));
        warn.push(...(warns?.series ?? []).filter((item) => ids.has(item.labels.check_id)));
      },
    ),
  );
  return { up: indexBy(up, 'check_id'), warn: indexBy(warn, 'check_id') };
};

/** Uptime, p95 and certificate days over the range, uptime leaving maintenance out. */
const rangeValues = async (
  group: CheckView[],
  inMaintenance: CheckView[],
  intervals: Map<string, Interval[]>,
  query: Query,
  window: { seconds: number; from: number; to: number; uptimeStep: number },
): Promise<RangeValues> => {
  const { seconds, from, to, uptimeStep } = window;
  const intervalOf = new Map(group.map((check) => [check.id, check.intervalSeconds]));
  const [[uptime, p95, cert], [passed, runs]] = await Promise.all([
    queryInChunks(group, (ids) => {
      const only = idsMatcher(ids, group.length);
      const resolution = quantileResolution(Math.min(...ids.map((id) => intervalOf.get(id) ?? 60)));
      return Promise.all([
        query.instant(passRatio('check_id', only, seconds)),
        query.instant(
          `max by (check_id) (${mergedQuantile(0.95, 'check_id, location', selector('synthetics_duration_seconds', 'phase="total"', only), seconds, resolution)})`,
        ),
        query.instant(
          `min by (check_id) (last_over_time(${selector('synthetics_cert_expiry_seconds', only)}[${CERT_WINDOW}]))`,
        ),
      ]);
    }),
    inMaintenance.length
      ? queryInChunks(inMaintenance, (ids) => {
          const only = idsMatcher(ids, group.length);
          const up = selector('synthetics_up', only);
          // The point at t covers (t - step, t]: start a step in so none
          // reaches before the range.
          return Promise.all([
            query.ranged(
              `sum by (check_id) (sum_over_time(${up}[${uptimeStep}s]))`,
              from + uptimeStep * 1000,
              to,
              uptimeStep,
            ),
            query.ranged(
              `sum by (check_id) (count_over_time(${up}[${uptimeStep}s]))`,
              from + uptimeStep * 1000,
              to,
              uptimeStep,
            ),
          ]);
        })
      : [EMPTY, EMPTY],
  ]);
  const values: RangeValues = {
    uptime: latestBy(uptime?.series ?? [], 'check_id'),
    p95: latestBy(p95?.series ?? [], 'check_id'),
    cert: latestBy(cert?.series ?? [], 'check_id'),
  };
  const passedBy = indexBy(passed?.series ?? [], 'check_id');
  const runsBy = indexBy(runs?.series ?? [], 'check_id');
  for (const check of inMaintenance) {
    values.uptime.set(
      check.id,
      uptimeExcluding(
        passedBy.get(check.id)?.[0]?.points ?? [],
        runsBy.get(check.id)?.[0]?.points ?? [],
        uptimeStep,
        intervals.get(check.id) ?? [],
      ),
    );
  }
  return values;
};

const scaled = (value: number | undefined, factor: number) =>
  value === undefined ? undefined : value * factor;
