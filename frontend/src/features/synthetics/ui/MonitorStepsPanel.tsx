import { LoadingOutlined } from '@ant-design/icons';
import { Spin, Table, Tooltip } from 'antd';
import type { EChartsOption } from 'echarts';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  type CheckView,
  type PromQLSeries,
  type RunResult,
  syntheticsApi,
} from '../../../entity/synthetics';
import { baseOption, timeAxis, valueAxis } from '../lib/chartOptions';
import { formatMs, formatPercent } from '../lib/format';
import { type StepRow, failedStepNote, stepRows } from '../lib/multiStep';
import { maxBy, mergedQuantile, quantileResolution, ratioBy, selector } from '../lib/queries';
import { rangeCache } from '../lib/rangeCache';
import { STATUS_COLOR } from '../lib/status';
import { RANGE_SECONDS, type RangeKey } from '../lib/useMonitorData';
import { type ChartColors, EChart } from './EChart';
import { Panel } from './parts';

interface Props {
  check: CheckView;
  range: RangeKey;
  /** A location to narrow to; undefined for all. */
  location?: string;
  /** Runs that weren't green, newest first, for each step's last failure. */
  failures: RunResult[];
  /** The failures are the newest few, not all in the range. */
  failuresCapped?: boolean;
  /** The page's refresh tick. */
  refresh?: number;
}

type Row = StepRow & { lastFailure?: string };

/** Per step and location over the range, so a location filter needs no new query. */
interface StepTotals {
  passed: PromQLSeries[];
  runs: PromQLSeries[];
  p50: PromQLSeries[];
  p95: PromQLSeries[];
}

interface StepData {
  totals: StepTotals;
  /** p95 per bucket, per step and location. */
  durations: PromQLSeries[];
  from: number;
  to: number;
}

const BY_STEP = ['step_index', 'step'];
const STEP_LOCATION = 'step_index, step, location';
const stepTotals = rangeCache<StepTotals>();

const STEP_COLORS = [
  '#34d399',
  '#60a5fa',
  '#fbbf24',
  '#a78bfa',
  '#f472b6',
  '#22d3ee',
  '#fb923c',
  '#a3e635',
  '#94a3b8',
  '#f87171',
];

/** Per-step success rate, p50, p95, share of run time and last failure, and p95 by step over time. */
export const MonitorStepsPanel = ({
  check,
  range,
  location,
  failures,
  failuresCapped,
  refresh = 0,
}: Props) => {
  const [data, setData] = useState<StepData>();
  const [error, setError] = useState<string>();
  const shownRange = useRef<RangeKey>(undefined);

  useEffect(() => {
    const controller = new AbortController();
    const { siteId, projectId } = check.destination;
    const to = Date.now();
    const seconds = RANGE_SECONDS[range];
    const from = to - seconds * 1000;
    const bucket = Math.max(60, Math.round(seconds / 120));
    const resolution = quantileResolution(check.intervalSeconds);
    const only = `check_id="${check.id}"`;
    const up = selector('synthetics_step_up', only);
    const duration = selector('synthetics_step_duration_seconds', only);
    const instant = (query: string) =>
      syntheticsApi.promql(siteId, projectId, { query, instant: true }, controller.signal);
    if (shownRange.current !== range) {
      shownRange.current = range;
      setData(undefined);
      setError(undefined);
    }
    Promise.all([
      stepTotals.load(`${check.id}|${check.version}|${siteId}/${projectId}|${range}`, async () => {
        const [passed, runs, p50, p95] = await Promise.all([
          instant(`sum by (${STEP_LOCATION}) (sum_over_time(${up}[${seconds}s]))`),
          instant(`sum by (${STEP_LOCATION}) (count_over_time(${up}[${seconds}s]))`),
          instant(mergedQuantile(0.5, STEP_LOCATION, duration, seconds, resolution)),
          instant(mergedQuantile(0.95, STEP_LOCATION, duration, seconds, resolution)),
        ]);
        return { passed: passed.series, runs: runs.series, p50: p50.series, p95: p95.series };
      }),
      syntheticsApi.promql(
        siteId,
        projectId,
        {
          query: mergedQuantile(0.95, STEP_LOCATION, duration, bucket, resolution),
          start: new Date(from).toISOString(),
          end: new Date(to).toISOString(),
          stepSeconds: bucket,
        },
        controller.signal,
      ),
    ])
      .then(([totals, durations]) => {
        setData({ totals, durations: durations.series, from, to });
        setError(undefined);
      })
      .catch((reason: Error) => {
        if (!controller.signal.aborted) setError(reason.message);
      });
    return () => controller.abort();
  }, [check, range, refresh]);

  const stepRowsShown = useMemo(() => {
    if (!data) return undefined;
    const here = (series: PromQLSeries[]) =>
      location ? series.filter((item) => item.labels.location === location) : series;
    const { passed, runs, p50, p95 } = data.totals;
    return stepRows(
      check.definition.steps,
      ratioBy(here(passed), here(runs), BY_STEP),
      here(p50),
      here(p95),
    );
  }, [data, location, check.definition.steps]);
  const durations = useMemo(
    () =>
      maxBy(
        (data?.durations ?? []).filter((item) => !location || item.labels.location === location),
        BY_STEP,
      ),
    [data, location],
  );

  const rows = useMemo(
    () =>
      (stepRowsShown ?? []).map((row) => {
        const failed = failures.find((run) =>
          run.steps?.some((step) => step.index === row.index && step.status === 'down'),
        );
        const step = failed?.steps?.find((item) => item.index === row.index);
        return {
          ...row,
          lastFailure:
            failed && step
              ? `${new Date(failed.startedAt).toLocaleString()} · ${failed.location}${step.httpStatus ? ` · ${step.httpStatus}` : ''}`
              : undefined,
        };
      }),
    [stepRowsShown, failures],
  );

  const chart = useCallback(
    (colors: ChartColors): EChartsOption => {
      const series = [...durations].sort(
        (a, b) => Number(a.labels.step_index) - Number(b.labels.step_index),
      );
      return {
        ...baseOption(colors),
        tooltip: {
          ...baseOption(colors).tooltip,
          valueFormatter: (value) => (value == null ? 'no data' : formatMs(Number(value))),
        },
        xAxis: timeAxis(colors, data?.from ?? 0, data?.to ?? 0),
        yAxis: valueAxis(colors, (value) => formatMs(value)),
        series: series.map((item) => ({
          name: `${item.labels.step_index}. ${item.labels.step}`,
          type: 'line',
          stack: 'steps',
          showSymbol: false,
          areaStyle: { opacity: 0.6 },
          lineStyle: { width: 0.8 },
          itemStyle: { color: colorOf(Number(item.labels.step_index)) },
          data: item.points.map((point) => [
            Date.parse(point.timestamp),
            Math.round(point.value * 1000),
          ]),
        })),
      };
    },
    [data, durations],
  );

  if (error) {
    return (
      <Panel title="Steps">
        <span className="text-sm text-red-600 dark:text-red-400">{error}</span>
      </Panel>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <Panel title="Steps" subtitle="Over the range; share of run time by p95">
        {!stepRowsShown ? (
          <div className="flex justify-center py-8">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        ) : (
          <Table<Row>
            size="small"
            pagination={false}
            rowKey="index"
            dataSource={rows}
            locale={{ emptyText: 'No step results in this range yet.' }}
            columns={[
              {
                title: 'Step',
                key: 'name',
                render: (_, row) => (
                  <span className="flex items-center gap-2">
                    <span
                      className="inline-block h-2 w-2 shrink-0 rounded-full"
                      style={{ background: colorOf(row.index) }}
                    />
                    {row.index}. {row.name}
                  </span>
                ),
              },
              {
                title: 'Success',
                key: 'success',
                align: 'right',
                render: (_, row) => (
                  <span
                    style={{
                      color:
                        row.success === undefined || row.success >= 0.995
                          ? undefined
                          : row.success >= 0.95
                            ? STATUS_COLOR.warn
                            : STATUS_COLOR.down,
                    }}
                  >
                    {formatPercent(row.success)}
                  </span>
                ),
              },
              { title: 'p50', key: 'p50', align: 'right', render: (_, row) => formatMs(row.p50Ms) },
              { title: 'p95', key: 'p95', align: 'right', render: (_, row) => formatMs(row.p95Ms) },
              {
                title: 'Share of run time',
                key: 'share',
                render: (_, row) => (
                  <Tooltip title={formatPercent(row.share, 0)}>
                    <div className="h-2 w-full min-w-16 rounded bg-gray-100 dark:bg-gray-700">
                      <div
                        className="h-2 rounded"
                        style={{
                          width: `${Math.round((row.share ?? 0) * 100)}%`,
                          background: colorOf(row.index),
                        }}
                      />
                    </div>
                  </Tooltip>
                ),
              },
              {
                title: 'Last failure',
                key: 'lastFailure',
                render: (_, row) => (
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {row.lastFailure ??
                      (failuresCapped ? 'none in the newest failed runs' : 'none in the range')}
                  </span>
                ),
              },
            ]}
          />
        )}
      </Panel>
      <Panel title="Duration by step" subtitle="p95 per bucket, stacked">
        <EChart option={chart} height={240} />
      </Panel>
    </div>
  );
};

/** Dots per step for the recent runs table, with the failing step named. */
export const RunSteps = ({ run }: { run: RunResult }) => (
  <span className="inline-flex items-center gap-1">
    {(run.steps ?? []).map((step) => (
      <Tooltip
        key={step.index}
        title={`${step.name}: ${step.status === 'skipped' ? 'skipped' : step.httpStatus ? `${step.status}, ${step.httpStatus}` : step.status}${step.afterFailure ? ' (clean-up)' : ''}`}
      >
        <span
          className="inline-block h-2.5 w-2.5 rounded-full"
          role="img"
          aria-label={`${step.name}: ${step.status}`}
          style={{
            background:
              step.status === 'skipped'
                ? '#d1d5db'
                : STATUS_COLOR[step.status === 'up' ? 'up' : step.status],
          }}
        />
      </Tooltip>
    ))}
    {failedStepNote(run) && (
      <span className="ml-1 text-xs text-gray-500 dark:text-gray-400">{failedStepNote(run)}</span>
    )}
  </span>
);

const colorOf = (index: number) =>
  STEP_COLORS[(index - 1 + STEP_COLORS.length) % STEP_COLORS.length];
