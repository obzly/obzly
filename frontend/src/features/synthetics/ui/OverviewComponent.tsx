import { Button, Segmented } from 'antd';
import type { EChartsOption } from 'echarts';
import { memo, useCallback, useMemo, useState } from 'react';

import type { CheckView } from '../../../entity/synthetics';
import { type Better, absoluteChange } from '../../../shared/lib/periodChange';
import { Donut, KpiTile, RankedList } from '../../../shared/ui/v2';
import { baseOption, timeAxis, valueAxis } from '../lib/chartOptions';
import { formatMs, formatPercent } from '../lib/format';
import { LATENCY_BANDS, groupByFolder, hexRadius, latencyBand } from '../lib/honeycomb';
import {
  type MonitorLocations,
  failsSomewhere,
  statusAt,
  summarizeLocations,
} from '../lib/locationHealth';
import {
  type MonitorStatus,
  STATUS_COLOR,
  STATUS_LABEL,
  STATUS_ORDER,
  byText,
  locationStatus,
} from '../lib/status';
import { type MonitorDataResult, STRIP_SLOTS } from '../lib/useMonitorData';
import { type ChartColors, EChart } from './EChart';
import { Honeycomb, type HoneycombGroup, type HoneycombItem } from './Honeycomb';
import { MaintenanceMarker } from './MaintenanceMarker';
import { Panel, StatusBadge, StatusDot, Strip } from './parts';

interface Props {
  checks: CheckView[];
  result: MonitorDataResult;
  locations: string[];
  onOpen: (check: CheckView) => void;
}

const KPI_STATUSES: MonitorStatus[] = ['up', 'warn', 'down', 'impacted', 'maintenance', 'off'];
const OVERVIEW_LIMIT = 50;
// Location health lists only monitors failing somewhere; this many before "Show all".
const LOCATION_ATTENTION_LIMIT = 8;
// A "Show all" list scrolls inside its panel instead of making the page longer.
const SHOW_ALL_SCROLL = 'max-h-[480px] overflow-y-auto';
type AvailabilityView = 'honeycomb' | 'timeline';
type ColourBy = 'status' | 'p95';
const VIEW_KEY = 'obzly-synthetics-availability-view';
// Above this many monitors the honeycomb is the default; strips read better for a few.
const HONEYCOMB_FROM = 50;
const LEGEND_ORDER: MonitorStatus[] = [
  'down',
  'impacted',
  'warn',
  'maintenance',
  'up',
  'none',
  'off',
];
// p95 bands: the accent from light to full, then amber and red for slow.
const LATENCY_COLOR = [
  'color-mix(in srgb, var(--v2-accent) 35%, var(--v2-panel2))',
  'var(--v2-accent)',
  STATUS_COLOR.warn,
  STATUS_COLOR.down,
];
const NO_TIMINGS = 'var(--v2-border)';

const savedView = (): AvailabilityView | null => {
  try {
    const value = localStorage.getItem(VIEW_KEY);
    return value === 'honeycomb' || value === 'timeline' ? value : null;
  } catch {
    return null;
  }
};

/** A monitor's cell coloured by its status now. */
const statusCell = (id: string, name: string, status: MonitorStatus): HoneycombItem => ({
  id,
  label: `${name}: ${STATUS_LABEL[status]}`,
  fill: STATUS_COLOR[status],
  half: status === 'impacted',
  outline: status === 'off' ? STATUS_COLOR.off : undefined,
});
// Which way is good news for a status tile's change; Maintenance and Off are planned, so no pill.
const KPI_BETTER: Partial<Record<MonitorStatus, Better>> = {
  up: 'up',
  warn: 'down',
  down: 'down',
  impacted: 'down',
};
const KPI_NOTE: Partial<Record<MonitorStatus, string>> = {
  impacted: 'a parent is down',
  maintenance: 'alerts muted',
};

const ShowAllToggle = ({
  total,
  showAll,
  onChange,
}: {
  total: number;
  showAll: boolean;
  onChange: (showAll: boolean) => void;
}) =>
  total > OVERVIEW_LIMIT ? (
    <Button type="link" size="small" onClick={() => onChange(!showAll)}>
      {showAll ? `Show first ${OVERVIEW_LIMIT}` : `Show all ${total}`}
    </Button>
  ) : null;

const NO_SLOTS = Array(STRIP_SLOTS).fill('none');

/** "24 h", "7 d": how far back a change pill compares. */
const agoLabel = (seconds: number) =>
  seconds >= 2 * 86400 ? `${Math.round(seconds / 86400)} d` : `${Math.round(seconds / 3600)} h`;

const Overview = ({ checks, result, locations, onOpen }: Props) => {
  const { data, from, stepSeconds, previous } = result;
  const statusOf = useCallback(
    (check: CheckView): MonitorStatus => data.get(check.id)?.state.status ?? 'none',
    [data],
  );
  const counts = useMemo(() => {
    const byStatus = new Map<MonitorStatus, number>();
    for (const check of checks) {
      const status = statusOf(check);
      byStatus.set(status, (byStatus.get(status) ?? 0) + 1);
    }
    return KPI_STATUSES.map((status) => ({ status, count: byStatus.get(status) ?? 0 }));
  }, [checks, statusOf]);
  const worstFirst = useMemo(
    () =>
      [...checks].sort(
        (a, b) => STATUS_ORDER[statusOf(a)] - STATUS_ORDER[statusOf(b)] || byText(a.name, b.name),
      ),
    [checks, statusOf],
  );
  const enabled = useMemo(() => checks.filter((check) => check.enabled), [checks]);
  const enabledWorstFirst = useMemo(
    () => worstFirst.filter((check) => check.enabled),
    [worstFirst],
  );
  // Big lists show the worst few: a strip or row per monitor for 1,000
  // monitors is a lot of DOM to re-render every minute.
  const [showAllStrips, setShowAllStrips] = useState(false);
  const [showAllLocations, setShowAllLocations] = useState(false);
  const [viewChoice, setViewChoice] = useState<AvailabilityView | null>(savedView);
  const view: AvailabilityView =
    viewChoice ?? (checks.length > HONEYCOMB_FROM ? 'honeycomb' : 'timeline');
  const chooseView = (next: AvailabilityView) => {
    setViewChoice(next);
    try {
      localStorage.setItem(VIEW_KEY, next);
    } catch {
      // The choice still holds for this visit.
    }
  };
  const [colourBy, setColourBy] = useState<ColourBy>('status');
  const [byFolder, setByFolder] = useState(false);
  const [statusFilter, setStatusFilter] = useState<MonitorStatus | null>(null);
  const hasFolders = useMemo(() => checks.some((check) => check.folder), [checks]);
  const monitorLocations = useMemo(
    () =>
      new Map<string, MonitorLocations>(
        enabledWorstFirst.map((check) => [
          check.id,
          {
            id: check.id,
            locations: data.get(check.id)?.state.locations ?? [],
            inMaintenance: !!data.get(check.id)?.maintenance?.active,
          },
        ]),
      ),
    [enabledWorstFirst, data],
  );
  const locationSummary = useMemo(
    () => summarizeLocations(locations, [...monitorLocations.values()]),
    [locations, monitorLocations],
  );
  const failingSomewhere = useMemo(
    () => enabledWorstFirst.filter((check) => failsSomewhere(monitorLocations.get(check.id)!)),
    [enabledWorstFirst, monitorLocations],
  );
  const locationRows = showAllLocations
    ? enabledWorstFirst
    : failingSomewhere.slice(0, LOCATION_ATTENTION_LIMIT);
  const checksById = useMemo(() => new Map(checks.map((check) => [check.id, check])), [checks]);
  const openById = useCallback(
    (id: string) => {
      const check = checksById.get(id);
      if (check) onOpen(check);
    },
    [checksById, onOpen],
  );

  const availabilityGroups = useMemo((): HoneycombGroup[] => {
    const cell = (check: CheckView): HoneycombItem => {
      if (colourBy === 'p95') {
        const p95 = data.get(check.id)?.p95Ms;
        const band = latencyBand(p95);
        return {
          id: check.id,
          label: `${check.name}: p95 ${p95 === undefined ? 'no timings' : formatMs(Math.round(p95))}`,
          fill: band < 0 ? NO_TIMINGS : LATENCY_COLOR[band],
        };
      }
      const status = statusOf(check);
      return {
        ...statusCell(check.id, check.name, status),
        dimmed: statusFilter !== null && status !== statusFilter,
      };
    };
    if (!byFolder || !hasFolders) {
      return [{ key: 'all', items: worstFirst.map(cell) }];
    }
    return groupByFolder(worstFirst).map(({ folder, items }) => {
      const failing = items.filter((check) =>
        ['down', 'warn', 'impacted'].includes(statusOf(check)),
      ).length;
      return {
        key: folder || '(top level)',
        title: `${folder || 'No folder'} · ${items.length} ${items.length === 1 ? 'monitor' : 'monitors'}${failing ? ` · ${failing} need${failing === 1 ? 's' : ''} a look` : ''}`,
        items: items.map(cell),
      };
    });
  }, [worstFirst, colourBy, byFolder, hasFolders, statusFilter, data, statusOf]);

  const statusCounts = useMemo(() => {
    const byStatus = new Map<MonitorStatus, number>();
    for (const check of checks) {
      byStatus.set(statusOf(check), (byStatus.get(statusOf(check)) ?? 0) + 1);
    }
    return LEGEND_ORDER.filter((status) => byStatus.get(status)).map((status) => ({
      status,
      count: byStatus.get(status) ?? 0,
    }));
  }, [checks, statusOf]);
  const latencyCounts = useMemo(() => {
    const bands = LATENCY_BANDS.map(() => 0);
    let none = 0;
    for (const check of checks) {
      const band = latencyBand(data.get(check.id)?.p95Ms);
      if (band < 0) none++;
      else bands[band]++;
    }
    return { bands, none };
  }, [checks, data]);

  // One honeycomb per location, every monitor in the same place in each.
  const locationGroups = useMemo(
    () =>
      locations.map((location) => ({
        location,
        items: enabledWorstFirst.map((check): HoneycombItem => {
          const monitor = monitorLocations.get(check.id)!;
          const state = monitor.locations.find((s) => s.location === location);
          if (!state) {
            return {
              id: check.id,
              label: `${check.name} at ${location}: no runs`,
              fill: NO_TIMINGS,
            };
          }
          const status = statusAt(state, monitor.inMaintenance);
          return {
            ...statusCell(check.id, check.name, status),
            label: `${check.name} at ${location}: ${STATUS_LABEL[status]}`,
          };
        }),
      })),
    [locations, enabledWorstFirst, monitorLocations],
  );

  const monitorTooltip = useCallback(
    (id: string) => {
      const check = checksById.get(id);
      const monitor = data.get(id);
      if (!check) return null;
      const status = statusOf(check);
      const failing = (monitor?.state.locations ?? [])
        .filter((state) => locationStatus(state) !== 'up')
        .map((state) => `${state.location}: ${STATUS_LABEL[locationStatus(state)].toLowerCase()}`);
      return (
        <div className="flex flex-col gap-1">
          <div className="font-semibold break-words">{check.name}</div>
          {check.folder && <div className="opacity-70">{check.folder}</div>}
          <div className="flex justify-between gap-2">
            <span>{STATUS_LABEL[status]}</span>
            <span className="tabular-nums">
              {formatPercent(monitor?.uptime)} up · p95{' '}
              {monitor?.p95Ms === undefined ? '–' : formatMs(Math.round(monitor.p95Ms))}
            </span>
          </div>
          {failing.length > 0 && <div className="break-words">Failing: {failing.join(', ')}</div>}
          <div className="mt-1">
            <Strip slots={monitor?.strip ?? NO_SLOTS} slotSeconds={stepSeconds} from={from} />
          </div>
        </div>
      );
    },
    [checksById, data, statusOf, stepSeconds, from],
  );

  // Share of monitors passing everywhere, per slot (monitors without runs or in maintenance in a slot don't count).
  const upShare = useMemo(
    () =>
      Array.from({ length: STRIP_SLOTS }, (_, slot) => {
        const states = enabled
          .map((check) => data.get(check.id)?.strip[slot])
          .filter((state) => state && state !== 'none' && state !== 'maintenance');
        const up = states.filter((state) => state !== 'down').length;
        return states.length ? Math.round((up / states.length) * 1000) / 10 : null;
      }),
    [enabled, data],
  );
  const trend = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      legend: { show: false },
      xAxis: timeAxis(colors, from, from + STRIP_SLOTS * stepSeconds * 1000),
      yAxis: { ...valueAxis(colors, (value) => `${value}%`), min: 0, max: 100 },
      series: [
        {
          type: 'line',
          name: 'Monitors up',
          data: upShare.map((value, slot) => [from + slot * stepSeconds * 1000, value]),
          // Few points early on: show them as dots.
          showSymbol: true,
          symbolSize: 4,
          connectNulls: false,
          // Share of monitors up: a health measure, so the "up" colour in every theme.
          itemStyle: { color: colors.status.ok },
          lineStyle: { width: 2, color: colors.status.ok },
          areaStyle: { color: colors.status.ok, opacity: 0.12 },
        },
      ],
    }),
    [upShare, from, stepSeconds],
  );
  const upTrend = useMemo(
    () => upShare.filter((value): value is number => value !== null),
    [upShare],
  );

  const slowest = useMemo(
    () =>
      enabled
        .filter((check) => data.get(check.id)?.p95Ms !== undefined)
        .sort((a, b) => (data.get(b.id)?.p95Ms ?? 0) - (data.get(a.id)?.p95Ms ?? 0))
        .slice(0, 10),
    [enabled, data],
  );
  const outages = useMemo(
    () => worstFirst.filter((check) => ['down', 'warn'].includes(statusOf(check))),
    [worstFirst, statusOf],
  );
  const certificates = useMemo(
    () =>
      enabled
        .filter((check) => (data.get(check.id)?.certDays ?? Infinity) < 30)
        .sort((a, b) => (data.get(a.id)?.certDays ?? 0) - (data.get(b.id)?.certDays ?? 0)),
    [enabled, data],
  );

  return (
    <div className="flex flex-col gap-4">
      <section
        aria-label="Status"
        className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-4"
      >
        <KpiTile label="Monitors" value={checks.length} note={`${locations.length} locations`} />
        {counts.map(({ status, count }) => (
          <KpiTile
            key={status}
            label={STATUS_LABEL[status]}
            value={
              <span style={count > 0 ? { color: STATUS_COLOR[status] } : undefined}>{count}</span>
            }
            icon={<StatusDot status={status} size={12} />}
            note={KPI_NOTE[status]}
            change={
              previous && KPI_BETTER[status]
                ? absoluteChange(
                    count,
                    previous.counts[status] ?? 0,
                    KPI_BETTER[status],
                    (difference) => `${difference} vs ${agoLabel(previous.seconds)} ago`,
                  )
                : undefined
            }
            // The Up tile carries the share-passing trend from the strips.
            trend={status === 'up' ? upTrend : undefined}
          />
        ))}
      </section>

      {/* Outages and expiring certificates come first: they need action now. */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Active outages" subtitle="Monitors failing or degraded now">
          {outages.length === 0 && (
            <div className="text-sm text-gray-500">None. Everything is passing.</div>
          )}
          {outages.map((check) => {
            const failing = (data.get(check.id)?.state.locations ?? []).filter(
              (state) => locationStatus(state) !== 'up',
            );
            return (
              <div
                key={check.id}
                className="flex cursor-pointer items-center justify-between gap-3 border-b border-gray-100 py-2.5 last:border-0 dark:border-gray-700"
                onClick={() => onOpen(check)}
              >
                <span className="min-w-0">
                  <span className="font-semibold">{check.name}</span>
                  <span className="block truncate text-xs text-gray-500 dark:text-gray-400">
                    {failing
                      .map(
                        (state) =>
                          `${state.location}: ${STATUS_LABEL[locationStatus(state)].toLowerCase()}`,
                      )
                      .join(', ')}
                  </span>
                </span>
                <StatusBadge status={statusOf(check)} />
              </div>
            );
          })}
        </Panel>
        <Panel title="Certificates expiring soon" subtitle="Within 30 days">
          {certificates.length === 0 && (
            <div className="text-sm text-gray-500">None within 30 days.</div>
          )}
          {certificates.map((check) => {
            const days = Math.floor(data.get(check.id)?.certDays ?? 0);
            const status: MonitorStatus = days < 7 ? 'down' : days < 21 ? 'warn' : 'up';
            return (
              <div
                key={check.id}
                className="flex cursor-pointer items-center justify-between gap-3 border-b border-gray-100 py-2.5 last:border-0 dark:border-gray-700"
                onClick={() => onOpen(check)}
              >
                <span className="min-w-0 truncate">{check.name}</span>
                <span
                  className="rounded-full px-2 py-0.5 text-xs font-semibold tabular-nums"
                  style={{ color: STATUS_COLOR[status], background: `${STATUS_COLOR[status]}1f` }}
                >
                  {days} days
                </span>
              </div>
            );
          })}
        </Panel>
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Panel title="Status" subtitle="Every monitor, now">
          <div className="flex flex-wrap items-center gap-5">
            <Donut
              label={`Status of ${checks.length} monitors`}
              center={checks.length}
              centerSub="monitors"
              parts={counts
                .filter(({ count }) => count > 0)
                .map(({ status, count }) => ({
                  key: status,
                  value: count,
                  color: STATUS_COLOR[status],
                }))}
            />
            <ul className="m-0 flex min-w-[140px] flex-1 list-none flex-col gap-2 p-0 text-sm">
              {counts.map(({ status, count }) => (
                <li key={status} className="flex items-center gap-2">
                  <StatusDot status={status} size={9} />
                  <span className="flex-1 text-gray-600 dark:text-gray-300">
                    {STATUS_LABEL[status]}
                  </span>
                  <strong className="font-semibold tabular-nums">{count}</strong>
                </li>
              ))}
            </ul>
          </div>
        </Panel>
        <Panel
          title="Monitors up"
          subtitle="Share of monitors passing from every location"
          className="lg:col-span-2"
        >
          <EChart option={trend} height={220} />
        </Panel>
      </div>

      <Panel
        title="Availability"
        subtitle={
          view === 'honeycomb'
            ? colourBy === 'p95'
              ? 'Every monitor by p95 response time over the range. Hover for details, click to open one.'
              : 'Every monitor, failing ones first. Hover for recent runs, click to open one.'
            : worstFirst.length > OVERVIEW_LIMIT && !showAllStrips
              ? `The first ${OVERVIEW_LIMIT} of ${worstFirst.length} monitors, failing ones first. Click one to open it.`
              : 'Every monitor, failing ones first. Click one to open it.'
        }
        extra={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Segmented<AvailabilityView>
              size="small"
              value={view}
              onChange={chooseView}
              options={[
                { value: 'honeycomb', label: 'Honeycomb' },
                { value: 'timeline', label: 'Timeline' },
              ]}
            />
            {view === 'honeycomb' && (
              <Segmented<ColourBy>
                size="small"
                value={colourBy}
                onChange={(next) => {
                  setColourBy(next);
                  setStatusFilter(null);
                }}
                options={[
                  { value: 'status', label: 'Status' },
                  { value: 'p95', label: 'p95' },
                ]}
              />
            )}
            {view === 'honeycomb' && hasFolders && (
              <Segmented<string>
                size="small"
                value={byFolder ? 'folder' : 'none'}
                onChange={(next) => setByFolder(next === 'folder')}
                options={[
                  { value: 'none', label: 'No groups' },
                  { value: 'folder', label: 'By folder' },
                ]}
              />
            )}
            {view === 'timeline' && (
              <ShowAllToggle
                total={worstFirst.length}
                showAll={showAllStrips}
                onChange={setShowAllStrips}
              />
            )}
          </div>
        }
      >
        {checks.length === 0 && <div className="text-sm text-gray-500">No monitors yet.</div>}
        {checks.length > 0 && view === 'honeycomb' && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="Legend">
              {colourBy === 'status'
                ? statusCounts.map(({ status, count }) => (
                    <button
                      key={status}
                      type="button"
                      aria-pressed={statusFilter === status}
                      onClick={() => setStatusFilter(statusFilter === status ? null : status)}
                      className={`focus-visible:outline-accent-500 inline-flex cursor-pointer items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs tabular-nums focus-visible:outline-2 ${statusFilter === status ? 'border-accent-500 bg-accent-500/10' : 'border-gray-200 bg-transparent dark:border-gray-700'}`}
                    >
                      <StatusDot status={status} size={9} />
                      {STATUS_LABEL[status]} <strong className="font-semibold">{count}</strong>
                    </button>
                  ))
                : [
                    ...LATENCY_BANDS.map((band, index) => (
                      <span
                        key={band.label}
                        className="inline-flex items-center gap-1.5 text-xs text-gray-700 tabular-nums dark:text-gray-200"
                      >
                        <span
                          className="inline-block h-2.5 w-5 rounded-sm"
                          style={{ background: LATENCY_COLOR[index] }}
                        />
                        {band.label}{' '}
                        <strong className="font-semibold">{latencyCounts.bands[index]}</strong>
                      </span>
                    )),
                    <span
                      key="none"
                      className="inline-flex items-center gap-1.5 text-xs text-gray-500 tabular-nums dark:text-gray-400"
                    >
                      <span
                        className="inline-block h-2.5 w-5 rounded-sm"
                        style={{ background: NO_TIMINGS }}
                      />
                      no timings {latencyCounts.none}
                    </span>,
                  ]}
            </div>
            <Honeycomb
              groups={availabilityGroups}
              radius={hexRadius(checks.length)}
              ariaLabel="Monitors"
              tooltip={monitorTooltip}
              onOpen={openById}
            />
            <div className="text-xs text-gray-500 dark:text-gray-400">
              {colourBy === 'p95'
                ? 'Colour is p95 response time over the range. Slowest monitors ranks the top 10.'
                : 'Colour is the status now. A half-filled cell is impacted: failing while a parent is down.'}
            </div>
          </div>
        )}
        {checks.length > 0 && view === 'timeline' && (
          <div className={`flex flex-col gap-1.5 ${showAllStrips ? SHOW_ALL_SCROLL : ''}`}>
            {(showAllStrips ? worstFirst : worstFirst.slice(0, OVERVIEW_LIMIT)).map((check) => (
              <div
                key={check.id}
                className="grid cursor-pointer grid-cols-[minmax(140px,240px)_minmax(0,1fr)_64px] items-center gap-3 rounded-lg px-1.5 py-0.5 hover:bg-gray-50 dark:hover:bg-gray-700/50"
                onClick={() => onOpen(check)}
              >
                <span className="flex min-w-0 items-center gap-2 truncate">
                  <StatusDot status={statusOf(check)} tooltip />
                  <span className="truncate">{check.name}</span>
                  <MaintenanceMarker maintenance={data.get(check.id)?.maintenance} />
                </span>
                <Strip
                  slots={data.get(check.id)?.strip ?? NO_SLOTS}
                  slotSeconds={stepSeconds}
                  from={from}
                />
                <span className="text-right text-xs text-gray-500 tabular-nums dark:text-gray-400">
                  {formatPercent(data.get(check.id)?.uptime)}
                </span>
              </div>
            ))}
          </div>
        )}
      </Panel>

      {/* items-start: Slowest monitors keeps its own height next to a long Location health. */}
      <div className="grid items-start gap-4 lg:grid-cols-2">
        <Panel
          title="Location health"
          subtitle={
            showAllLocations
              ? 'Latest run per monitor and location'
              : 'One honeycomb per location, each monitor in the same place; failing monitors below'
          }
          extra={
            enabledWorstFirst.length > 0 ? (
              <Button
                type="link"
                size="small"
                onClick={() => setShowAllLocations(!showAllLocations)}
              >
                {showAllLocations ? 'Show failing only' : `Show all ${enabledWorstFirst.length}`}
              </Button>
            ) : null
          }
        >
          <div className="grid gap-x-5 gap-y-4 sm:grid-cols-2">
            {locationGroups.map(({ location, items }) => {
              const summary = locationSummary.find((entry) => entry.location === location);
              const total = summary?.total ?? 0;
              const counts = summary?.counts;
              return (
                <div key={location} className="min-w-0">
                  <div className="mb-1.5 flex justify-between gap-2 text-xs">
                    <span
                      className="truncate font-mono text-gray-700 dark:text-gray-200"
                      title={location}
                    >
                      {location}
                    </span>
                    <span className="whitespace-nowrap text-gray-500 tabular-nums dark:text-gray-400">
                      {total === 0 || !counts
                        ? 'no runs'
                        : `${counts.up} / ${total} passing${counts.down ? ` · ${counts.down} down` : ''}`}
                    </span>
                  </div>
                  <Honeycomb
                    groups={[{ key: location, items }]}
                    radius={hexRadius(items.length, true)}
                    gap={1.5}
                    ariaLabel={`Monitors at ${location}`}
                    tooltip={monitorTooltip}
                    onOpen={openById}
                  />
                </div>
              );
            })}
          </div>
          {!showAllLocations && failingSomewhere.length === 0 && enabledWorstFirst.length > 0 && (
            <div className="mt-3 text-sm text-gray-500">
              Every monitor passes from every location.
            </div>
          )}
          {locationRows.length > 0 && (
            <div className={`mt-3 overflow-x-auto ${showAllLocations ? SHOW_ALL_SCROLL : ''}`}>
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs tracking-wide text-gray-500 dark:text-gray-400">
                    <th className="py-1 pr-2 font-medium">
                      {showAllLocations ? 'Monitor' : 'Failing somewhere'}
                    </th>
                    {locations.map((location) => (
                      <th key={location} className="px-2 py-1 font-mono font-medium normal-case">
                        {location}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {locationRows.map((check) => {
                    const monitor = monitorLocations.get(check.id)!;
                    const states = new Map(
                      monitor.locations.map((state) => [state.location, state]),
                    );
                    return (
                      <tr
                        key={check.id}
                        className="cursor-pointer border-t border-gray-100 hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-700/50"
                        onClick={() => onOpen(check)}
                      >
                        <td className="max-w-[220px] truncate py-1.5 pr-2" title={check.name}>
                          {check.name}
                        </td>
                        {locations.map((location) => {
                          const state = states.get(location);
                          return (
                            <td key={location} className="px-2 py-1.5">
                              {state ? (
                                <StatusDot
                                  status={statusAt(state, monitor.inMaintenance)}
                                  tooltip
                                />
                              ) : (
                                <span className="text-gray-400">–</span>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {!showAllLocations && failingSomewhere.length > LOCATION_ATTENTION_LIMIT && (
            <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
              {failingSomewhere.length - LOCATION_ATTENTION_LIMIT} more failing somewhere ·{' '}
              <Button
                type="link"
                size="small"
                className="!p-0"
                onClick={() => setShowAllLocations(true)}
              >
                show all
              </Button>
            </div>
          )}
        </Panel>
        <Panel title="Slowest monitors" subtitle="p95 response time over the range">
          <RankedList
            empty="No timings yet."
            rows={slowest.map((check) => {
              const value = Math.round(data.get(check.id)?.p95Ms ?? 0);
              return {
                key: check.id,
                label: check.name,
                value,
                display: formatMs(value),
                color:
                  data.get(check.id)?.state.status === 'down'
                    ? STATUS_COLOR.down
                    : value > 800
                      ? STATUS_COLOR.warn
                      : 'var(--v2-accent)',
                onClick: () => onOpen(check),
              };
            })}
          />
        </Panel>
      </div>
    </div>
  );
};

/** Memoised: the page re-renders on every poll, the overview only when its data changed. */
export const OverviewComponent = memo(Overview);
