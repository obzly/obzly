import {
  CopyOutlined,
  EditOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { Alert, App, Button, Drawer, Modal, Spin, Table, Tag } from 'antd';
import type { EChartsOption } from 'echarts';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  type CheckView,
  type MaintenanceOccurrence,
  type PromQLSeries,
  type RunResult,
  type TestRun,
  syntheticsApi,
} from '../../../entity/synthetics';
import { baseOption, timeAxis, valueAxis } from '../lib/chartOptions';
import { formatInterval, formatMs, formatPercent, targetOf } from '../lib/format';
import { heatmapGrid, hourLabel, hourWindow } from '../lib/heatmap';
import { heatmapTooltip } from '../lib/heatmapTooltip';
import { durationText, intervalsFor, markMaintenance } from '../lib/maintenance';
import { failedStep, failedStepNote } from '../lib/multiStep';
import { PHASE_COLORS, PHASE_LABELS } from '../lib/phases';
import {
  indexBy,
  latestBy,
  mean,
  mergedQuantile,
  passRatio,
  quantileResolution,
  sameSeries,
  selector,
} from '../lib/queries';
import { rangeCache } from '../lib/rangeCache';
import { type MonitorStatus, STATUS_COLOR, downThreshold } from '../lib/status';
import { useLocationUptime } from '../lib/useLocationUptime';
import {
  type MonitorData,
  RANGE_SECONDS,
  type RangeKey,
  STRIP_SLOTS,
  type SlotStatus,
  locationStrip,
  stripWindow,
} from '../lib/useMonitorData';
import { type ChartColors, EChart } from './EChart';
import { MaintenanceBanner } from './MaintenanceBanner';
import { MonitorStepsPanel, RunSteps } from './MonitorStepsPanel';
import { RunDetail, TestResults } from './RunDetailParts';
import { IconAction, Panel, StatusBadge, StatusDot, Strip, TypeTag } from './parts';

interface Props {
  check: CheckView;
  monitor?: MonitorData;
  range: RangeKey;
  canManage: boolean;
  onClose: () => void;
  onEdit: (check: CheckView) => void;
  onClone: (check: CheckView) => void;
  onToggle: (check: CheckView) => void;
  /** Every monitor, for its parents and children. */
  checks: CheckView[];
  statusOf: (check: CheckView) => MonitorStatus;
  onOpenCheck: (check: CheckView) => void;
  /** Maintenance occurrences, for strips, uptime and the banner. */
  maintenance?: MaintenanceOccurrence[];
  onEndMaintenance?: (windowId: string) => Promise<void>;
  onOpenMaintenance?: (windowId: string) => void;
  /** The page's refresh tick: the drawer reloads with it. */
  refresh?: number;
}

/** What reloads on every refresh. */
interface DetailData {
  from: number;
  to: number;
  /** Now at load: charts end here, strips at `to`. */
  now: number;
  step: number;
  stripStep: number;
  stripUp: PromQLSeries[];
  stripWarn: PromQLSeries[];
  locationStrips: Map<string, SlotStatus[]>;
  latency: PromQLSeries[];
  phases: PromQLSeries[];
  size: PromQLSeries[];
  redirects: PromQLSeries[];
  icmpRtt: PromQLSeries[];
  icmpLoss: PromQLSeries[];
  /** The newest RUN_LIMIT runs. */
  runs: RunResult[];
  /** The newest FAILURE_LIMIT runs that weren't green, over the whole range. */
  failures: RunResult[];
}

/** Values over the whole range, reloaded at most every 5 minutes. */
interface RangeDetail {
  heatFrom: number;
  heat: PromQLSeries[];
  uptimeByLocation: Map<string, number>;
  p50ByLocation: Map<string, number>;
  p95ByLocation: Map<string, number>;
  p99ByLocation: Map<string, number>;
}

const LOCATION_COLORS = [
  '#059669',
  '#8b5cf6',
  '#f97316',
  '#0ea5e9',
  '#ec4899',
  '#84cc16',
  '#eab308',
  '#64748b',
];
const RUN_LIMIT = 500;
const FAILURE_LIMIT = 500;
const NO_MAINTENANCE: MaintenanceOccurrence[] = [];
const NO_RUNS: RunResult[] = [];
const CLOCK = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const SINCE = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});
// Shared by every drawer opened, so reopening a monitor within 5 minutes
// doesn't query the range again.
const rangeDetails = rangeCache<RangeDetail>();

export const MonitorDetailDrawer = ({
  check,
  monitor,
  range,
  canManage,
  onClose,
  onEdit,
  onClone,
  onToggle,
  checks,
  statusOf,
  onOpenCheck,
  maintenance = NO_MAINTENANCE,
  onEndMaintenance,
  onOpenMaintenance,
  refresh = 0,
}: Props) => {
  const { message } = App.useApp();
  const [detail, setDetail] = useState<DetailData>();
  const [rangeDetail, setRangeDetail] = useState<RangeDetail>();
  const [error, setError] = useState<string>();
  const shownRange = useRef<RangeKey>(undefined);
  const [location, setLocation] = useState<string>();
  const [openRun, setOpenRun] = useState<RunResult>();
  const [testRun, setTestRun] = useState<TestRun>();
  const [testing, setTesting] = useState(false);
  const status: MonitorStatus = monitor?.state.status ?? 'none';
  const [ending, setEnding] = useState(false);
  const activeMaintenance = monitor?.maintenance?.active;
  const maintenanceUptime = useLocationUptime(check, range, maintenance);
  const locationUptime = (name: string) =>
    maintenanceUptime?.get(name) ?? rangeDetail?.uptimeByLocation.get(name);
  const markedStrips = useMemo(() => {
    if (!detail) return new Map<string, SlotStatus[]>();
    const intervals = intervalsFor(check.id, maintenance, detail.from, detail.to);
    return new Map(
      [...detail.locationStrips].map(([name, slots]) => [
        name,
        markMaintenance(slots, detail.from, detail.stripStep, intervals),
      ]),
    );
  }, [detail, maintenance, check.id]);
  const shownStrip = (name: string) => markedStrips.get(name) ?? [];
  const endMaintenance = async () => {
    if (!activeMaintenance || !onEndMaintenance) return;
    setEnding(true);
    try {
      await onEndMaintenance(activeMaintenance.windowId);
    } finally {
      setEnding(false);
    }
  };

  useEffect(() => {
    const controller = new AbortController();
    const { siteId, projectId } = check.destination;
    const now = Date.now();
    const seconds = RANGE_SECONDS[range];
    const { from, to, step: stripStep } = stripWindow(seconds, now);
    const step = Math.max(60, Math.round(seconds / 120));
    const only = `check_id="${check.id}"`;
    const duration = selector('synthetics_duration_seconds', only, 'phase="total"');
    // Charts end now; strips run to the end of the current slot.
    const ranged = (query: string, stepSeconds = step, start = from, end = now) =>
      syntheticsApi.promql(
        siteId,
        projectId,
        {
          query,
          start: new Date(start).toISOString(),
          end: new Date(end).toISOString(),
          stepSeconds,
        },
        controller.signal,
      );
    const instant = (query: string) =>
      syntheticsApi.promql(siteId, projectId, { query, instant: true }, controller.signal);
    const runs = (limit: number, failuresOnly: boolean) =>
      syntheticsApi.runs(
        siteId,
        projectId,
        check.id,
        new Date(from),
        new Date(now),
        limit,
        controller.signal,
        failuresOnly,
      );
    // A refresh keeps what is shown until the new numbers arrive; a new
    // range starts over.
    if (shownRange.current !== range) {
      shownRange.current = range;
      setDetail(undefined);
      setRangeDetail(undefined);
      setError(undefined);
    }

    const loadRange = async (): Promise<RangeDetail> => {
      const hours = hourWindow(seconds, now);
      const resolution = quantileResolution(check.intervalSeconds);
      const quantile = (value: number) =>
        instant(mergedQuantile(value, 'location', duration, seconds, resolution));
      const [heat, uptime, p50, p95, p99] = await Promise.all([
        ranged(passRatio('location', only, 3600), 3600, hours.from, hours.to),
        instant(passRatio('location', only, seconds)),
        quantile(0.5),
        quantile(0.95),
        quantile(0.99),
      ]);
      return {
        heatFrom: hours.from,
        heat: heat.series,
        uptimeByLocation: latestBy(uptime.series, 'location'),
        p50ByLocation: latestBy(p50.series, 'location'),
        p95ByLocation: latestBy(p95.series, 'location'),
        p99ByLocation: latestBy(p99.series, 'location'),
      };
    };

    Promise.all([
      ranged(
        `min by (location) (min_over_time(${selector('synthetics_up', only)}[${stripStep}s]))`,
        stripStep,
        from,
        to,
      ),
      ranged(
        `max by (location) (max_over_time(${selector('synthetics_warn', only)}[${stripStep}s]))`,
        stripStep,
        from,
        to,
      ),
      ranged(mean('location', duration, step)),
      ranged(mean('phase', selector('synthetics_duration_seconds', only, 'phase!="total"'), step)),
      ranged(mean('', selector('synthetics_response_bytes', only), step)),
      ranged(`max(max_over_time(${selector('synthetics_redirects', only)}[${step}s]))`),
      ranged(mean('location', selector('synthetics_icmp_rtt_seconds', only), step)),
      ranged(
        `max by (location) (max_over_time(${selector('synthetics_icmp_loss_ratio', only)}[${step}s]))`,
      ),
      runs(RUN_LIMIT, false),
      runs(FAILURE_LIMIT, true),
      rangeDetails.load(`${check.id}|${check.version}|${siteId}/${projectId}|${range}`, loadRange),
    ])
      .then(
        ([
          stripUp,
          stripWarn,
          latency,
          phases,
          size,
          redirects,
          icmpRtt,
          icmpLoss,
          newestRuns,
          failures,
          values,
        ]) => {
          const upBy = indexBy(stripUp.series, 'location');
          const warnBy = indexBy(stripWarn.series, 'location');
          const next: DetailData = {
            from,
            to,
            now,
            step,
            stripStep,
            stripUp: stripUp.series,
            stripWarn: stripWarn.series,
            locationStrips: new Map(
              [...upBy.keys()]
                .sort()
                .map((name) => [
                  name,
                  locationStrip(
                    upBy.get(name)?.[0]?.points,
                    warnBy.get(name)?.[0]?.points,
                    from,
                    stripStep,
                    check.intervalSeconds,
                  ),
                ]),
            ),
            latency: latency.series,
            phases: phases.series,
            size: size.series,
            redirects: redirects.series,
            icmpRtt: icmpRtt.series,
            icmpLoss: icmpLoss.series,
            runs: newestRuns,
            failures,
          };
          // Unchanged numbers keep the old objects: nothing re-renders.
          setDetail((previous) => (previous && sameDetail(previous, next) ? previous : next));
          setRangeDetail(values);
          setError(undefined);
        },
      )
      .catch((reason: Error) => {
        if (!controller.signal.aborted) setError(reason.message);
      });
    return () => controller.abort();
  }, [check, range, refresh]);

  const shownLocations = useMemo(
    () =>
      detail
        ? [...detail.locationStrips.keys()].filter((name) => !location || name === location)
        : [],
    [detail, location],
  );
  const runs = useMemo(
    () => (detail?.runs ?? NO_RUNS).filter((run) => !location || run.location === location),
    [detail, location],
  );
  const failures = useMemo(
    () => (detail?.failures ?? NO_RUNS).filter((run) => !location || run.location === location),
    [detail, location],
  );
  // The run lists are capped: say what they cover rather than imply the whole range.
  const runsCapped = (detail?.runs.length ?? 0) >= RUN_LIMIT;
  const failuresCapped = (detail?.failures.length ?? 0) >= FAILURE_LIMIT;
  const since = (list: RunResult[]) =>
    list.length ? SINCE.format(Date.parse(list[list.length - 1].startedAt)) : '';
  const quantiles = useMemo(
    () => ({
      p50: msOf(highest(rangeDetail?.p50ByLocation)),
      p95: msOf(highest(rangeDetail?.p95ByLocation)),
      p99: msOf(highest(rangeDetail?.p99ByLocation)),
    }),
    [rangeDetail],
  );

  const runNow = async () => {
    setTesting(true);
    try {
      const run = await syntheticsApi.createTest({
        checkId: check.id,
        check: {
          name: check.name,
          enabled: check.enabled,
          intervalSeconds: check.intervalSeconds,
          downLocations: check.downLocations,
          locations: check.locations,
          destinationId: check.destination.id,
          definition: check.definition,
        },
        locations: check.locations.length ? check.locations : undefined,
      });
      setTestRun(run);
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setTesting(false);
    }
  };

  const colorFor = (name: string) => {
    const index = [...(detail?.locationStrips.keys() ?? [])].indexOf(name);
    return LOCATION_COLORS[(index < 0 ? 0 : index) % LOCATION_COLORS.length];
  };
  const filterSeries = (series: PromQLSeries[]) =>
    series.filter((item) => !location || item.labels.location === location);

  const latencyChart = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      tooltip: {
        ...baseOption(colors).tooltip,
        valueFormatter: (value) => (value == null ? 'no data' : formatMs(Number(value))),
      },
      xAxis: timeAxis(colors, detail?.from ?? 0, detail?.now ?? 0),
      yAxis: valueAxis(colors, (value) => formatMs(value)),
      series: filterSeries(detail?.latency ?? []).map((item) => ({
        name: item.labels.location,
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 1.6 },
        itemStyle: { color: colorFor(item.labels.location) },
        data: item.points.map((point) => [
          Date.parse(point.timestamp),
          Math.round(point.value * 1000),
        ]),
      })),
    }),

    [detail, location],
  );

  const phaseChart = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      tooltip: {
        ...baseOption(colors).tooltip,
        valueFormatter: (value) => formatMs(Number(value)),
      },
      xAxis: timeAxis(colors, detail?.from ?? 0, detail?.now ?? 0),
      yAxis: valueAxis(colors, (value) => formatMs(value)),
      series: ['dns', 'connect', 'tls', 'ttfb', 'transfer']
        .map((phase) => (detail?.phases ?? []).find((item) => item.labels.phase === phase))
        .filter((item): item is PromQLSeries => !!item)
        .map((item) => ({
          name: PHASE_LABELS[item.labels.phase] ?? item.labels.phase,
          type: 'line',
          stack: 'phases',
          showSymbol: false,
          areaStyle: { opacity: 0.55 },
          lineStyle: { width: 0.8 },
          itemStyle: { color: PHASE_COLORS[item.labels.phase] },
          data: item.points.map((point) => [
            Date.parse(point.timestamp),
            Math.round(point.value * 1000),
          ]),
        })),
    }),
    [detail],
  );

  const heatChart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const { names, hours, cells } = heatmapGrid(
        filterSeries(rangeDetail?.heat ?? []),
        rangeDetail?.heatFrom ?? 0,
      );
      return {
        animation: false,
        grid: { left: 120, right: 16, top: 8, bottom: 52 },
        tooltip: {
          position: 'top',
          backgroundColor: colors.tooltip,
          borderColor: colors.axis,
          textStyle: { color: colors.text },
          formatter: heatmapTooltip(names, hours),
        },
        xAxis: {
          type: 'category',
          data: hours.map((hour) => hourLabel(hour, range === '7d')),
          axisLabel: { color: colors.text, fontSize: 10 },
          axisLine: { lineStyle: { color: colors.axis } },
        },
        yAxis: {
          type: 'category',
          data: names,
          axisLabel: { color: colors.text, fontSize: 11 },
          axisLine: { show: false },
          axisTick: { show: false },
        },
        visualMap: {
          min: 50,
          max: 100,
          orient: 'horizontal',
          left: 'center',
          bottom: 0,
          itemHeight: 120,
          itemWidth: 10,
          calculable: false,
          text: ['100%', '≤50%'],
          textStyle: { color: colors.text, fontSize: 11 },
          inRange: { color: [STATUS_COLOR.down, STATUS_COLOR.warn, STATUS_COLOR.up] },
        },
        series: [
          {
            type: 'heatmap',
            data: cells,
            itemStyle: { borderColor: colors.tooltip, borderWidth: 2, borderRadius: 2 },
          },
        ],
      };
    },

    [rangeDetail, location, range],
  );

  const codes = useMemo(() => {
    const counts = new Map<string, number>();
    for (const run of runs) {
      const failedAt = failedStep(run);
      const key = failedAt
        ? `failed at ${failedAt.name}`
        : run.httpStatus
          ? String(run.httpStatus)
          : run.error
            ? 'no response'
            : 'ok';
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
    return [...counts].sort((a, b) => b[1] - a[1]);
  }, [runs]);
  const codesChart = useCallback(
    (colors: ChartColors): EChartsOption => ({
      tooltip: {
        trigger: 'item',
        backgroundColor: colors.tooltip,
        borderColor: colors.axis,
        textStyle: { color: colors.text },
        formatter: '{b}: {c} runs ({d}%)',
      },
      legend: {
        orient: 'vertical',
        right: 0,
        top: 'middle',
        textStyle: { color: colors.text, fontSize: 11 },
      },
      series: [
        {
          type: 'pie',
          radius: ['48%', '76%'],
          center: ['36%', '50%'],
          label: { show: false },
          itemStyle: { borderColor: colors.tooltip, borderWidth: 2 },
          data: codes.map(([code, count]) => ({
            name: code,
            value: count,
            itemStyle: {
              color:
                /^[23]/.test(code) || code === 'ok'
                  ? STATUS_COLOR.up
                  : /^4/.test(code)
                    ? STATUS_COLOR.warn
                    : code === 'no response'
                      ? '#94a3b8'
                      : STATUS_COLOR.down,
            },
          })),
        },
      ],
    }),
    [codes],
  );

  const reasons = useMemo(() => {
    const counts = new Map<string, { count: number; warn: boolean }>();
    for (const run of failures) {
      if (run.status === 'up') continue;
      const reason = failureReason(run);
      const current = counts.get(reason) ?? { count: 0, warn: run.status === 'warn' };
      counts.set(reason, { count: current.count + 1, warn: current.warn && run.status === 'warn' });
    }
    return [...counts].sort((a, b) => b[1].count - a[1].count).slice(0, 8);
  }, [failures]);
  const reasonsChart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const rows = [...reasons].reverse();
      return {
        ...baseOption(colors),
        legend: { show: false },
        grid: { left: 140, right: 36, top: 6, bottom: 22 },
        tooltip: { ...baseOption(colors).tooltip, axisPointer: { type: 'shadow' } },
        xAxis: { ...valueAxis(colors), minInterval: 1 },
        yAxis: {
          type: 'category',
          data: rows.map(([reason]) => reason),
          axisLabel: { color: colors.text, fontSize: 11, width: 130, overflow: 'truncate' },
          axisLine: { show: false },
          axisTick: { show: false },
        },
        series: [
          {
            type: 'bar',
            barWidth: 12,
            label: { show: true, position: 'right', color: colors.text, fontSize: 11 },
            data: rows.map(([, value]) => ({
              value: value.count,
              itemStyle: {
                color: value.warn ? STATUS_COLOR.warn : STATUS_COLOR.down,
                borderRadius: [0, 3, 3, 0],
              },
            })),
          },
        ],
      };
    },
    [reasons],
  );

  const sizeChart = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      xAxis: timeAxis(colors, detail?.from ?? 0, detail?.now ?? 0),
      yAxis: [
        valueAxis(colors, (value) => `${(value / 1024).toFixed(1)} KiB`),
        { ...valueAxis(colors), minInterval: 1, splitLine: { show: false } },
      ],
      series: [
        {
          name: 'bytes',
          type: 'line',
          showSymbol: false,
          itemStyle: { color: '#059669' },
          data: (detail?.size[0]?.points ?? []).map((point) => [
            Date.parse(point.timestamp),
            point.value,
          ]),
        },
        {
          name: 'redirects',
          type: 'line',
          step: 'end',
          yAxisIndex: 1,
          showSymbol: false,
          itemStyle: { color: '#8b5cf6' },
          data: (detail?.redirects[0]?.points ?? []).map((point) => [
            Date.parse(point.timestamp),
            point.value,
          ]),
        },
      ],
    }),
    [detail],
  );

  const icmpChart = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      xAxis: timeAxis(colors, detail?.from ?? 0, detail?.now ?? 0),
      yAxis: [
        valueAxis(colors, (value) => formatMs(value)),
        {
          ...valueAxis(colors, (value) => `${Math.round(value * 100)}%`),
          max: 1,
          splitLine: { show: false },
        },
      ],
      series: [
        ...filterSeries(detail?.icmpRtt ?? []).map((item) => ({
          name: `${item.labels.location} rtt`,
          type: 'line' as const,
          showSymbol: false,
          itemStyle: { color: colorFor(item.labels.location) },
          data: item.points.map((point) => [Date.parse(point.timestamp), point.value * 1000]),
        })),
        ...filterSeries(detail?.icmpLoss ?? []).map((item) => ({
          name: `${item.labels.location} loss`,
          type: 'bar' as const,
          yAxisIndex: 1,
          barMaxWidth: 6,
          itemStyle: { color: STATUS_COLOR.down },
          data: item.points.map((point) => [Date.parse(point.timestamp), point.value]),
        })),
      ],
    }),

    [detail, location],
  );

  const latestTLS = runs.find((run) => run.tls)?.tls;
  const outages = useMemo(
    () =>
      detail
        ? findOutages(detail, shownLocations, failures, downThreshold(check, shownLocations.length))
        : [],
    [detail, shownLocations, failures, check],
  );
  const lastRun = runs[0];
  const locationsTotal = detail?.locationStrips.size ?? 0;

  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(1180, Math.round(window.innerWidth * 0.94))}
      destroyOnHidden
      title={
        <div className="flex flex-wrap items-center gap-3">
          <StatusDot status={status} size={14} tooltip />
          <span className="text-lg font-semibold">{check.name}</span>
          <TypeTag type={check.type} />
          {activeMaintenance && (
            <StatusBadge
              status="maintenance"
              suffix={`until ${CLOCK.format(Date.parse(activeMaintenance.end))}`}
            />
          )}
          <span className="font-mono text-xs font-normal text-gray-500 dark:text-gray-400">
            {targetOf(check.definition)} · every {formatInterval(check.intervalSeconds)} ·{' '}
            {check.locations.length ? check.locations.join(', ') : 'all locations'}
          </span>
        </div>
      }
      extra={
        canManage && (
          <span className="inline-flex items-center gap-0.5">
            {activeMaintenance && onEndMaintenance && (
              <Button size="small" className="mr-1" loading={ending} onClick={endMaintenance}>
                End maintenance now
              </Button>
            )}
            <IconAction
              title="Run now"
              icon={<ThunderboltOutlined />}
              onClick={runNow}
              loading={testing}
            />
            <IconAction title="Edit" icon={<EditOutlined />} onClick={() => onEdit(check)} />
            <IconAction title="Clone" icon={<CopyOutlined />} onClick={() => onClone(check)} />
            <IconAction
              title={check.enabled ? 'Disable' : 'Enable'}
              icon={check.enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />}
              onClick={() => onToggle(check)}
            />
          </span>
        )
      }
    >
      {error && (
        <Alert
          type="error"
          showIcon
          className="mb-4"
          title={`Can't read results from ${check.destination.name}`}
          description={error}
        />
      )}
      <div className="mb-4 grid grid-cols-2 overflow-hidden rounded-lg border border-gray-200 sm:grid-cols-4 lg:grid-cols-7 dark:border-gray-700">
        <Stat label="Status" value={<StatusBadge status={status} />} />
        <Stat
          label="Uptime"
          value={formatPercent(monitor?.uptime, 2)}
          note={
            monitor?.maintenance?.excludedMs
              ? `excludes ${durationText(Math.round(monitor.maintenance.excludedMs / 60000))} maintenance`
              : undefined
          }
        />
        <Stat label="p50" value={formatMs(quantiles.p50)} />
        <Stat label="p95" value={formatMs(quantiles.p95)} />
        <Stat label="p99" value={formatMs(quantiles.p99)} />
        <Stat
          label="Certificate"
          value={monitor?.certDays === undefined ? '—' : `${Math.floor(monitor.certDays)} days`}
        />
        <Stat
          label="Last run"
          value={lastRun ? new Date(lastRun.startedAt).toLocaleTimeString() : '—'}
        />
      </div>

      {monitor?.impactedBy && (
        <Alert
          type="warning"
          showIcon
          className="mb-4"
          title={`Impacted: ${monitor.impactedBy.map((parent) => parent.name).join(', ')} ${monitor.impactedBy.length > 1 ? 'are' : 'is'} down`}
          description="Alerts for this monitor are muted until its parent recovers (when both send results to the same project)."
        />
      )}
      <MaintenanceBanner
        check={check}
        maintenance={maintenance}
        range={range}
        onOpen={onOpenMaintenance}
      />
      <Dependencies check={check} checks={checks} statusOf={statusOf} onOpen={onOpenCheck} />

      {!detail && !error && (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {detail && (
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap gap-2">
            <Tag.CheckableTag checked={!location} onChange={() => setLocation(undefined)}>
              All locations
            </Tag.CheckableTag>
            {[...detail.locationStrips.keys()].map((name) => (
              <Tag.CheckableTag
                key={name}
                checked={location === name}
                onChange={() => setLocation(location === name ? undefined : name)}
              >
                {name}
              </Tag.CheckableTag>
            ))}
          </div>

          <Panel
            title="Availability by location"
            subtitle={`Red from ${downThreshold(check, locationsTotal)} of ${locationsTotal} locations failing`}
          >
            <div className="flex flex-col gap-1.5">
              {shownLocations.map((name) => (
                <div
                  key={name}
                  className="grid grid-cols-[minmax(120px,200px)_minmax(0,1fr)_64px] items-center gap-3"
                >
                  <span className="flex items-center gap-2 truncate font-mono text-xs">
                    <span
                      className="inline-block h-2 w-2 rounded-full"
                      style={{ background: colorFor(name) }}
                    />
                    {name}
                  </span>
                  <Strip
                    slots={shownStrip(name)}
                    slotSeconds={detail.stripStep}
                    from={detail.from}
                  />
                  <span className="text-right text-xs text-gray-500 tabular-nums">
                    {formatPercent(locationUptime(name))}
                  </span>
                </div>
              ))}
              {shownLocations.length === 0 && (
                <span className="text-sm text-gray-500">No runs in this range yet.</span>
              )}
            </div>
            {outages.length > 0 && (
              <ul className="mt-3 mb-0 flex list-none flex-col gap-1 p-0 text-sm">
                {outages.map((outage) => (
                  <li key={outage.start} className="flex items-baseline gap-2">
                    <StatusDot status={outage.status} />
                    <span>
                      <b className="font-semibold">{new Date(outage.start).toLocaleString()}</b>,{' '}
                      {formatDuration(outage.end - outage.start)}
                      <span className="text-gray-500 dark:text-gray-400">
                        {' '}
                        · {outage.locations.join(', ')}
                        {outage.error ? ` · ${outage.error}` : ''}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          {check.type === 'multi' && (
            <MonitorStepsPanel
              check={check}
              range={range}
              location={location}
              failures={failures}
              failuresCapped={failuresCapped}
              refresh={refresh}
            />
          )}

          <div className="grid gap-4 lg:grid-cols-2">
            <Panel
              title="Response time by location"
              subtitle="Average per bucket"
              className={check.type === 'multi' ? 'lg:col-span-2' : ''}
            >
              <EChart option={latencyChart} height={280} />
            </Panel>
            {check.type === 'multi' ? null : check.type === 'icmp' ? (
              <Panel title="Round trip and loss" subtitle="ICMP">
                <EChart option={icmpChart} height={280} />
              </Panel>
            ) : (
              <Panel title="Where the time goes" subtitle="Average per phase, all locations">
                <EChart option={phaseChart} height={280} />
              </Panel>
            )}
          </div>

          <div className="grid gap-4 lg:grid-cols-3">
            <Panel
              title="Availability heatmap"
              subtitle="Share of runs that passed, per location and hour"
              className="lg:col-span-2"
            >
              <EChart option={heatChart} height={Math.max(180, shownLocations.length * 34 + 70)} />
            </Panel>
            <Panel title="Certificate" subtitle="From the latest run with TLS">
              {latestTLS ? (
                <TLSPanel
                  tls={latestTLS}
                  warnDays={check.definition.tls?.warnDays ?? 21}
                  verify={check.definition.tls?.verify !== false}
                />
              ) : (
                <span className="text-sm text-gray-500">No TLS in these runs.</span>
              )}
            </Panel>
          </div>

          <div className="grid gap-4 lg:grid-cols-3">
            <Panel
              title={check.type === 'http' ? 'Status codes' : 'Outcomes'}
              subtitle={
                runsCapped
                  ? `Newest ${runs.length} runs, since ${since(detail.runs)}`
                  : `All ${runs.length} runs in the range`
              }
            >
              {codes.length ? (
                <EChart option={codesChart} height={180} />
              ) : (
                <span className="text-sm text-gray-500">No runs.</span>
              )}
            </Panel>
            <Panel
              title="Failure reasons"
              subtitle={
                failuresCapped
                  ? `Newest ${failures.length} runs that weren't green, since ${since(detail.failures)}`
                  : "Runs that weren't green in the range"
              }
            >
              {reasons.length ? (
                <EChart option={reasonsChart} height={180} />
              ) : (
                <span className="text-sm text-gray-500">No failures. 🎉</span>
              )}
            </Panel>
            {check.type === 'multi' ? null : check.type === 'http' ? (
              <Panel title="Response size and redirects">
                <EChart option={sizeChart} height={180} />
              </Panel>
            ) : (
              <Panel title="Latest answer">
                <div className="font-mono text-xs break-all">
                  {lastRun?.dnsAnswers?.join(', ') || lastRun?.resolvedIps?.join(', ') || '—'}
                </div>
              </Panel>
            )}
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <Panel title="Locations">
              <Table
                size="small"
                pagination={false}
                rowKey="location"
                dataSource={[...detail.locationStrips.keys()].map((name) => {
                  const latest = detail.runs.find((run) => run.location === name);
                  const lastError = detail.failures.find((run) => run.location === name);
                  return { location: name, latest, lastError };
                })}
                columns={[
                  {
                    title: 'Location',
                    dataIndex: 'location',
                    render: (value: string) => <span className="font-mono text-xs">{value}</span>,
                  },
                  {
                    title: 'Latest',
                    key: 'latest',
                    render: (_, row) =>
                      row.latest ? <StatusBadge status={row.latest.status} /> : '—',
                  },
                  {
                    title: 'Uptime',
                    key: 'uptime',
                    align: 'right',
                    render: (_, row) => formatPercent(locationUptime(row.location)),
                  },
                  {
                    title: 'p95',
                    key: 'p95',
                    align: 'right',
                    render: (_, row) =>
                      formatMs(msOf(rangeDetail?.p95ByLocation.get(row.location))),
                  },
                  {
                    title: 'Last problem',
                    key: 'error',
                    render: (_, row) => (
                      <span className="text-xs text-gray-500 dark:text-gray-400">
                        {row.lastError
                          ? `${failureReason(row.lastError)} · ${new Date(row.lastError.startedAt).toLocaleString()}`
                          : '—'}
                      </span>
                    ),
                  },
                ]}
              />
            </Panel>
            <Panel title="Recent runs" subtitle="Click a run for its waterfall and assertions">
              <Table<RunResult>
                size="small"
                rowKey={(run) => `${run.startedAt}-${run.location}`}
                dataSource={runs}
                pagination={{ pageSize: 10, size: 'small' }}
                onRow={(run) => ({ onClick: () => setOpenRun(run), className: 'cursor-pointer' })}
                columns={[
                  {
                    title: 'Time',
                    key: 'time',
                    render: (_, run) => (
                      <span className="tabular-nums">
                        {new Date(run.startedAt).toLocaleString()}
                      </span>
                    ),
                  },
                  {
                    title: 'Location',
                    dataIndex: 'location',
                    render: (value: string) => <span className="font-mono text-xs">{value}</span>,
                  },
                  {
                    title: 'Result',
                    key: 'status',
                    render: (_, run) => <StatusBadge status={run.status} />,
                  },
                  check.type === 'multi'
                    ? { title: 'Steps', key: 'steps', render: (_, run) => <RunSteps run={run} /> }
                    : {
                        title: 'Code',
                        dataIndex: 'httpStatus',
                        align: 'right',
                        render: (value?: number) => value ?? '—',
                      },
                  {
                    title: 'Total',
                    dataIndex: 'durationMs',
                    align: 'right',
                    render: (value: number) => formatMs(value),
                  },
                ]}
              />
            </Panel>
          </div>
        </div>
      )}

      <Modal
        open={!!openRun}
        onCancel={() => setOpenRun(undefined)}
        footer={null}
        width={640}
        title="Run detail"
        destroyOnHidden
      >
        {openRun && <RunDetail run={openRun} />}
      </Modal>
      <Modal
        open={!!testRun}
        onCancel={() => setTestRun(undefined)}
        footer={null}
        width={680}
        title="Run now"
        destroyOnHidden
      >
        {testRun && <TestResults run={testRun} />}
      </Modal>
    </Drawer>
  );
};

/** The monitors this one needs and the ones that need it. */
const Dependencies = ({
  check,
  checks,
  statusOf,
  onOpen,
}: {
  check: CheckView;
  checks: CheckView[];
  statusOf: (check: CheckView) => MonitorStatus;
  onOpen: (check: CheckView) => void;
}) => {
  const parents = (check.parentIds ?? [])
    .map((id) => checks.find((other) => other.id === id))
    .filter((parent): parent is CheckView => !!parent);
  const children = checks.filter((other) => (other.parentIds ?? []).includes(check.id));
  const channels = check.alerts?.channelIds?.length ?? 0;
  const list = (items: CheckView[], empty: string) =>
    items.length === 0 ? (
      <span className="text-xs text-gray-500 dark:text-gray-400">{empty}</span>
    ) : (
      <span className="flex flex-wrap gap-1.5">
        {items.map((item) => (
          <button
            key={item.id}
            type="button"
            className="inline-flex cursor-pointer items-center gap-1.5 rounded-full border border-gray-200 bg-white px-2.5 py-0.5 text-xs text-gray-800 hover:border-emerald-500 dark:border-gray-600 dark:bg-gray-900 dark:text-gray-100"
            onClick={() => onOpen(item)}
          >
            <StatusDot status={statusOf(item)} size={8} />
            {item.name}
          </button>
        ))}
      </span>
    );
  return (
    <Panel
      className="mb-4"
      title="Dependencies"
      subtitle="While a parent is down this monitor shows Impacted and its alerts are muted"
      extra={
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Folder: {check.folder || 'none'} · Alerts:{' '}
          {channels === 0
            ? 'off'
            : `${channels} channel${channels > 1 ? 's' : ''}${check.alerts.notify === 'degraded' ? ', down or degraded' : ', down'}`}
        </span>
      }
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-gray-600 dark:text-gray-300">Depends on</span>
          {list(parents, 'No parents')}
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium text-gray-600 dark:text-gray-300">Needed by</span>
          {list(children, 'No monitors depend on it')}
        </div>
      </div>
    </Panel>
  );
};

const Stat = ({ label, value, note }: { label: string; value: React.ReactNode; note?: string }) => (
  <div className="border-r border-b border-gray-200 px-4 py-2.5 last:border-r-0 dark:border-gray-700">
    <div className="text-[11px] tracking-wide text-gray-500 uppercase dark:text-gray-400">
      {label}
    </div>
    <div className="text-lg font-semibold tabular-nums">{value}</div>
    {note && <div className="text-[11px] text-gray-500 dark:text-gray-400">{note}</div>}
  </div>
);

const TLSPanel = ({
  tls,
  warnDays,
  verify,
}: {
  tls: NonNullable<RunResult['tls']>;
  warnDays: number;
  verify: boolean;
}) => {
  const days = Math.floor(tls.daysLeft);
  const status: MonitorStatus = days <= 0 ? 'down' : days < warnDays ? 'warn' : 'up';
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-baseline gap-2">
        <span
          className="text-3xl font-semibold tabular-nums"
          style={{ color: STATUS_COLOR[status] }}
        >
          {days}
        </span>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          days until expiry (warn below {warnDays})
        </span>
      </div>
      <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs">
        <dt className="text-gray-500 dark:text-gray-400">Subject</dt>
        <dd className="m-0 font-mono break-all">{tls.subject}</dd>
        <dt className="text-gray-500 dark:text-gray-400">SANs</dt>
        <dd className="m-0 font-mono break-all">{tls.sans?.join(', ') || '—'}</dd>
        <dt className="text-gray-500 dark:text-gray-400">Issuer</dt>
        <dd className="m-0 font-mono break-all">
          {tls.issuer}
          {tls.selfSigned ? ' (self-signed)' : ''}
        </dd>
        <dt className="text-gray-500 dark:text-gray-400">Expires</dt>
        <dd className="m-0 font-mono">{new Date(tls.notAfter).toLocaleString()}</dd>
        <dt className="text-gray-500 dark:text-gray-400">Protocol</dt>
        <dd className="m-0 font-mono">
          {tls.protocol} · {tls.cipher}
        </dd>
        <dt className="text-gray-500 dark:text-gray-400">Verification</dt>
        <dd className="m-0 font-mono">
          {verify ? 'on' : 'off'},{' '}
          {tls.verified ? 'chain valid' : `invalid: ${tls.verifyError ?? '?'}`}
        </dd>
      </dl>
    </div>
  );
};

/** A short label for why a run wasn't green. */
const failureReason = (run: RunResult): string => {
  if (run.unsupported) return 'Unsupported on probe';
  if (run.steps?.length) {
    const note = failedStepNote(run);
    if (note) return note.charAt(0).toUpperCase() + note.slice(1);
  }
  if (run.error) {
    const error = run.error.toLowerCase();
    if (error.includes('timed out') || error.includes('deadline')) return 'Timeout';
    if (error.includes('resolve') || error.includes('no such host')) return 'DNS';
    if (error.includes('refused')) return 'Connection refused';
    if (error.includes('certificate') || error.includes('tls')) return 'TLS';
    return run.error.length > 40 ? `${run.error.slice(0, 39)}…` : run.error;
  }
  const failed = run.assertions?.find((assertion) => !assertion.passed);
  return failed ? failed.description : run.status === 'warn' ? 'Warning' : 'Failed';
};

interface Outage {
  start: number;
  end: number;
  status: MonitorStatus;
  locations: string[];
  error?: string;
}

/**
 * Contiguous slots where some location failed, with which locations and the
 * first error in it (from the failed runs, newest first), red from threshold
 * locations as the status.
 */
const findOutages = (
  detail: DetailData,
  locations: string[],
  failures: RunResult[],
  threshold: number,
): Outage[] => {
  const outages: Outage[] = [];
  let current: Outage | undefined;
  for (let slot = 0; slot < STRIP_SLOTS; slot++) {
    const failing = locations.filter((name) => {
      const value = detail.locationStrips.get(name)?.[slot];
      return value === 'down';
    });
    const start = detail.from + slot * detail.stripStep * 1000;
    if (failing.length > 0) {
      if (!current) {
        current = { start, end: start + detail.stripStep * 1000, status: 'warn', locations: [] };
        outages.push(current);
      }
      current.end = start + detail.stripStep * 1000;
      current.locations = [...new Set([...current.locations, ...failing])];
    } else {
      current = undefined;
    }
  }
  for (const outage of outages) {
    outage.status = outage.locations.length >= threshold ? 'down' : 'warn';
    const firstFailure = [...failures].reverse().find((run) => {
      const at = Date.parse(run.startedAt);
      return run.status === 'down' && at >= outage.start && at < outage.end;
    });
    outage.error = firstFailure ? failureReason(firstFailure) : undefined;
  }
  return outages.reverse().slice(0, 10);
};

/** The highest value, e.g. the overall p95 as the slowest location's. */
const highest = (values?: Map<string, number>) =>
  values && values.size ? Math.max(...values.values()) : undefined;

const sameRuns = (a: RunResult[], b: RunResult[]) =>
  a.length === b.length &&
  a.every(
    (run, index) =>
      run.startedAt === b[index].startedAt &&
      run.location === b[index].location &&
      run.status === b[index].status,
  );

const sameDetail = (a: DetailData, b: DetailData) =>
  a.from === b.from &&
  a.to === b.to &&
  sameSeries(a.stripUp, b.stripUp) &&
  sameSeries(a.stripWarn, b.stripWarn) &&
  sameSeries(a.latency, b.latency) &&
  sameSeries(a.phases, b.phases) &&
  sameSeries(a.size, b.size) &&
  sameSeries(a.redirects, b.redirects) &&
  sameSeries(a.icmpRtt, b.icmpRtt) &&
  sameSeries(a.icmpLoss, b.icmpLoss) &&
  sameRuns(a.runs, b.runs) &&
  sameRuns(a.failures, b.failures);

const msOf = (seconds?: number) => (seconds === undefined ? undefined : seconds * 1000);

const formatDuration = (ms: number): string => {
  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h ${minutes % 60} min`;
};
