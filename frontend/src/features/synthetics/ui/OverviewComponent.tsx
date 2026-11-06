import { Button } from 'antd';
import type { EChartsOption } from 'echarts';
import { memo, useCallback, useMemo, useState } from 'react';

import type { CheckView } from '../../../entity/synthetics';
import { baseOption, timeAxis, valueAxis } from '../lib/chartOptions';
import { formatMs, formatPercent } from '../lib/format';
import { withMaintenance } from '../lib/maintenance';
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
      {showAll ? `Show worst ${OVERVIEW_LIMIT}` : `Show all ${total}`}
    </Button>
  ) : null;

const NO_SLOTS = Array(STRIP_SLOTS).fill('none');

const Overview = ({ checks, result, locations, onOpen }: Props) => {
  const { data, from, stepSeconds } = result;
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

  const donut = useCallback(
    (colors: ChartColors): EChartsOption => ({
      tooltip: {
        trigger: 'item',
        backgroundColor: colors.tooltip,
        borderColor: colors.axis,
        textStyle: { color: colors.text },
      },
      title: {
        text: String(checks.length),
        subtext: 'monitors',
        left: 'center',
        top: '36%',
        textStyle: { color: colors.text, fontSize: 26, fontWeight: 600 },
        subtextStyle: { color: colors.text, fontSize: 12 },
      },
      series: [
        {
          type: 'pie',
          radius: ['58%', '80%'],
          label: { show: false },
          itemStyle: { borderColor: colors.tooltip, borderWidth: 2 },
          data: counts.map(({ status, count }) => ({
            name: STATUS_LABEL[status],
            value: count,
            itemStyle: { color: STATUS_COLOR[status], opacity: status === 'impacted' ? 0.55 : 1 },
          })),
        },
      ],
    }),

    [checks, counts],
  );

  // Share of monitors passing everywhere, per slot (monitors without runs or in maintenance in a slot don't count).
  const trend = useCallback(
    (colors: ChartColors): EChartsOption => {
      const points = Array.from({ length: STRIP_SLOTS }, (_, slot) => {
        const states = enabled
          .map((check) => data.get(check.id)?.strip[slot])
          .filter((state) => state && state !== 'none' && state !== 'maintenance');
        const up = states.filter((state) => state !== 'down').length;
        return [
          from + slot * stepSeconds * 1000,
          states.length ? Math.round((up / states.length) * 1000) / 10 : null,
        ];
      });
      return {
        ...baseOption(colors),
        legend: { show: false },
        xAxis: timeAxis(colors, from, from + STRIP_SLOTS * stepSeconds * 1000),
        yAxis: { ...valueAxis(colors, (value) => `${value}%`), min: 0, max: 100 },
        series: [
          {
            type: 'line',
            name: 'Monitors up',
            data: points,
            // Few points early on: show them as dots.
            showSymbol: true,
            symbolSize: 4,
            connectNulls: false,
            lineStyle: { width: 2, color: '#059669' },
            areaStyle: { color: 'rgba(5,150,105,0.12)' },
          },
        ],
      };
    },

    [enabled, data, from, stepSeconds],
  );

  const slowest = useMemo(
    () =>
      enabled
        .filter((check) => data.get(check.id)?.p95Ms !== undefined)
        .sort((a, b) => (data.get(b.id)?.p95Ms ?? 0) - (data.get(a.id)?.p95Ms ?? 0))
        .slice(0, 10),
    [enabled, data],
  );
  const slowChart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const rows = [...slowest].reverse();
      return {
        ...baseOption(colors),
        legend: { show: false },
        grid: { left: 160, right: 56, top: 6, bottom: 24 },
        tooltip: {
          ...baseOption(colors).tooltip,
          axisPointer: { type: 'shadow' },
          valueFormatter: (value) => formatMs(Number(value)),
        },
        xAxis: valueAxis(colors, (value) => formatMs(value)),
        yAxis: {
          type: 'category',
          data: rows.map((check) =>
            check.name.length > 24 ? `${check.name.slice(0, 23)}…` : check.name,
          ),
          axisLabel: { color: colors.text, fontSize: 11 },
          axisLine: { show: false },
          axisTick: { show: false },
        },
        series: [
          {
            type: 'bar',
            barWidth: 12,
            data: rows.map((check) => {
              const value = Math.round(data.get(check.id)?.p95Ms ?? 0);
              const color =
                data.get(check.id)?.state.status === 'down'
                  ? STATUS_COLOR.down
                  : value > 800
                    ? STATUS_COLOR.warn
                    : '#059669';
              return { value, itemStyle: { color, borderRadius: [0, 3, 3, 0] } };
            }),
            label: {
              show: true,
              position: 'right',
              color: colors.text,
              fontSize: 11,
              formatter: (params) => formatMs(Number(params.value)),
            },
          },
        ],
      };
    },

    [slowest, data],
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
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-7">
        <Panel title="Monitors">
          <div className="text-2xl font-semibold">{checks.length}</div>
          <div className="text-xs text-gray-500 dark:text-gray-400">
            {locations.length} locations
          </div>
        </Panel>
        {counts.map(({ status, count }) => (
          <Panel
            key={status}
            title={
              <span className="inline-flex items-center gap-2">
                <StatusDot status={status} />
                {STATUS_LABEL[status]}
              </span>
            }
          >
            <div className="text-2xl font-semibold">{count}</div>
          </Panel>
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Panel title="Status" subtitle="Every monitor, now">
          <EChart option={donut} height={220} />
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
          worstFirst.length > OVERVIEW_LIMIT && !showAllStrips
            ? `The ${OVERVIEW_LIMIT} worst of ${worstFirst.length} monitors. Click one to open it.`
            : 'Every monitor, worst first. Click one to open it.'
        }
        extra={
          <ShowAllToggle
            total={worstFirst.length}
            showAll={showAllStrips}
            onChange={setShowAllStrips}
          />
        }
      >
        <div className="flex flex-col gap-1.5">
          {(showAllStrips ? worstFirst : worstFirst.slice(0, OVERVIEW_LIMIT)).map((check) => (
            <div
              key={check.id}
              className="grid cursor-pointer grid-cols-[minmax(140px,240px)_minmax(0,1fr)_64px] items-center gap-3 rounded px-1 hover:bg-gray-50 dark:hover:bg-gray-700"
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
          {checks.length === 0 && <div className="text-sm text-gray-500">No monitors yet.</div>}
        </div>
      </Panel>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel
          title="Location health"
          subtitle={
            enabledWorstFirst.length > OVERVIEW_LIMIT && !showAllLocations
              ? `Latest run per location, the ${OVERVIEW_LIMIT} worst of ${enabledWorstFirst.length} monitors`
              : 'Latest run per monitor and location'
          }
          extra={
            <ShowAllToggle
              total={enabledWorstFirst.length}
              showAll={showAllLocations}
              onChange={setShowAllLocations}
            />
          }
        >
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-gray-500 uppercase dark:text-gray-400">
                  <th className="py-1 pr-2 font-medium">Monitor</th>
                  {locations.map((location) => (
                    <th key={location} className="px-2 py-1 font-mono font-medium normal-case">
                      {location}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(showAllLocations
                  ? enabledWorstFirst
                  : enabledWorstFirst.slice(0, OVERVIEW_LIMIT)
                ).map((check) => {
                  const states = new Map(
                    (data.get(check.id)?.state.locations ?? []).map((state) => [
                      state.location,
                      state,
                    ]),
                  );
                  return (
                    <tr key={check.id} className="border-t border-gray-100 dark:border-gray-700">
                      <td className="py-1.5 pr-2">{check.name}</td>
                      {locations.map((location) => {
                        const state = states.get(location);
                        return (
                          <td key={location} className="px-2 py-1.5">
                            {state ? (
                              <StatusDot
                                status={withMaintenance(
                                  locationStatus(state),
                                  !!data.get(check.id)?.maintenance?.active,
                                )}
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
        </Panel>
        <Panel title="Slowest monitors" subtitle="p95 response time over the range">
          {slowest.length ? (
            <EChart option={slowChart} height={Math.max(160, slowest.length * 28 + 30)} />
          ) : (
            <div className="text-sm text-gray-500">No timings yet.</div>
          )}
        </Panel>
      </div>

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
                className="flex cursor-pointer items-center justify-between gap-3 border-b border-dashed border-gray-200 py-2 last:border-0 dark:border-gray-700"
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
                className="flex items-center justify-between border-b border-dashed border-gray-200 py-2 last:border-0 dark:border-gray-700"
              >
                <span>{check.name}</span>
                <span
                  className="rounded-full px-2 text-xs font-semibold tabular-nums"
                  style={{ color: STATUS_COLOR[status], background: `${STATUS_COLOR[status]}1f` }}
                >
                  {days} days
                </span>
              </div>
            );
          })}
        </Panel>
      </div>
    </div>
  );
};

/** Memoised: the page re-renders on every poll, the overview only when its data changed. */
export const OverviewComponent = memo(Overview);
