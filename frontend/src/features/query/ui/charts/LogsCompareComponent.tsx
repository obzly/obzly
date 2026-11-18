import { LoadingOutlined } from '@ant-design/icons';
import { Button, DatePicker, Spin, Tooltip } from 'antd';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useState } from 'react';

import {
  type DeployMarker,
  type PatternChangesResponse,
  type QueryNode,
  queryApi,
} from '../../../../entity/query';
import { useTheme } from '../../../../shared/theme';
import { formatInTimezone, useDisplayTimezone } from '../../../../shared/time';
import { ChangePill } from '../../../../shared/ui/v2';
import { computeAutoBucketMs } from '../../../dashboards';
import {
  type CompareKind,
  comparisonWindows,
  deltaRows,
  ratioText,
  seriesOnGrid,
  timeGrid,
  topRows,
} from '../../lib/logsChart';
import type { TimeRange } from '../TimeRangePickerComponent';
import { ToggleGroupComponent } from '../ToggleGroupComponent';
import { LogsLineChartComponent } from './LogsLineChartComponent';
import { type PanelQuery, usePanelData } from './usePanelData';

interface Props {
  projectId: string;
  query: QueryNode | null;
  timeRange: TimeRange;
  refreshToken: number;
  /** The field "What changed" splits by. */
  split: string;
  compare: CompareKind;
  onCompareChange: (kind: CompareKind) => void;
  onFilterPattern: (hash: string) => void;
}

const KINDS: { value: CompareKind; label: string; name: string }[] = [
  { value: 'prev', label: 'Previous period', name: 'the previous period' },
  { value: 'day', label: 'Yesterday', name: 'the same time yesterday' },
  { value: 'week', label: 'Last week', name: 'the same time last week' },
  { value: 'deploy', label: 'Around a rollout', name: 'before the rollout' },
  { value: 'custom', label: 'Custom…', name: 'a custom range' },
];
// Groups per window for "What changed": both sides' largest groups.
const DELTA_GROUPS = 50;
const DELTA_ROWS = 10;
const MIN_LINES = 20;

const sum = (values: (number | null)[]) => values.reduce<number>((a, v) => a + (v ?? 0), 0);

/** Logs › Chart › Compare: the search now against an earlier window, overlaid, with what changed. */
export const LogsCompareComponent = ({
  projectId,
  query,
  timeRange,
  refreshToken,
  split,
  compare,
  onCompareChange,
  onFilterPattern,
}: Props): React.JSX.Element => {
  const { chartColors, definition, status } = useTheme();
  const [timezone] = useDisplayTimezone();
  const fromMs = timeRange.from.valueOf();
  const toMs = timeRange.to.valueOf();
  const [deploys, setDeploys] = useState<DeployMarker[] | null>(null);
  const [rolloutMs, setRolloutMs] = useState<number | undefined>();
  const [customStart, setCustomStart] = useState<dayjs.Dayjs | null>(null);
  const [patterns, setPatterns] = useState<PatternChangesResponse | null | 'error'>(null);

  useEffect(() => {
    if (compare !== 'deploy') return;
    const abort = new AbortController();
    setDeploys(null);
    queryApi
      .getDeploys(
        projectId,
        {
          query: query ?? undefined,
          timeRange: { from: new Date(fromMs).toISOString(), to: new Date(toMs).toISOString() },
        },
        abort.signal,
      )
      .then((response) => {
        const markers = [...response.markers].sort(
          (a, b) => Date.parse(b.time) - Date.parse(a.time),
        );
        setDeploys(markers);
        setRolloutMs((current) =>
          current !== undefined && markers.some((m) => Date.parse(m.time) === current)
            ? current
            : markers[0]
              ? Date.parse(markers[0].time)
              : undefined,
        );
      })
      .catch(() => !abort.signal.aborted && setDeploys([]));
    return () => abort.abort();
  }, [projectId, query, fromMs, toMs, compare, refreshToken]);

  const windows = comparisonWindows(compare, fromMs, toMs, {
    rolloutMs,
    customStartMs: customStart?.valueOf(),
    // Capped at the end of the search range.
    nowMs: toMs,
  });
  const length = windows ? windows.current.to - windows.current.from : toMs - fromMs;
  const bucketMs = computeAutoBucketMs(0, length, 60);
  const isDeploy = compare === 'deploy';

  // Around a rollout: one timeline across both halves. Otherwise each window apart.
  const series = (from: number, to: number): PanelQuery => ({
    panel: { type: 'line', query, bucketMs },
    from,
    to,
  });
  const groups = (from: number, to: number): PanelQuery => ({
    panel: { type: 'rankedList', query, groupField: split, limit: DELTA_GROUPS },
    from,
    to,
  });
  const [currentSeries, baselineSeries, currentGroups, baselineGroups] = usePanelData(
    projectId,
    windows
      ? [
          isDeploy
            ? series(windows.baseline.from, windows.current.to)
            : series(windows.current.from, windows.current.to),
          isDeploy ? null : series(windows.baseline.from, windows.baseline.to),
          groups(windows.current.from, windows.current.to),
          groups(windows.baseline.from, windows.baseline.to),
        ]
      : [null, null, null, null],
    refreshToken,
  );

  const windowsKey = windows ? JSON.stringify(windows) : '';
  useEffect(() => {
    if (!windowsKey) {
      setPatterns(null);
      return;
    }
    const parsed = JSON.parse(windowsKey) as NonNullable<typeof windows>;
    const iso = (ms: number) => new Date(ms).toISOString();
    const abort = new AbortController();
    setPatterns(null);
    queryApi
      .getPatternChanges(
        projectId,
        {
          query,
          current: { from: iso(parsed.current.from), to: iso(parsed.current.to) },
          baseline: { from: iso(parsed.baseline.from), to: iso(parsed.baseline.to) },
          limit: DELTA_ROWS,
        },
        abort.signal,
      )
      .then((response) => setPatterns(response))
      .catch(() => !abort.signal.aborted && setPatterns('error'));
    return () => abort.abort();
  }, [projectId, query, windowsKey, refreshToken]);

  const kindName = KINDS.find((k) => k.value === compare)?.name ?? '';
  const fmt = (ms: number, withDay = toMs - fromMs > 86_400_000 || compare !== 'prev') =>
    formatInTimezone(ms, withDay ? 'MMM D HH:mm' : 'HH:mm', timezone);
  const span = (w: { from: number; to: number }) => `${fmt(w.from)} – ${fmt(w.to)}`;

  // ---- the overlaid chart ----
  const chart = useMemo(() => {
    if (!windows) return null;
    if (isDeploy) {
      if (!currentSeries.buckets) return null;
      const grid = timeGrid(windows.baseline.from, windows.current.to, bucketMs);
      const values = seriesOnGrid(currentSeries.buckets, grid, 0)[0]?.values ?? grid.map(() => 0);
      const before = values.filter((_v, i) => grid[i] < windows.current.from);
      const average = before.length ? sum(before) / before.length : 0;
      const after = sum(values.filter((_v, i) => grid[i] >= windows.current.from));
      return {
        currentTotal: after,
        baselineTotal: sum(before),
        lines: [
          {
            name: 'This search',
            color: chartColors[0],
            area: true,
            points: grid.map((t, i) => [t, values[i]] as [number, number | null]),
          },
          {
            name: 'Before the rollout (average)',
            color: definition.colors.muted,
            dashed: true,
            points: grid.map((t) => [t, average] as [number, number | null]),
          },
        ],
      };
    }
    if (!currentSeries.buckets || !baselineSeries.buckets) return null;
    const currentGrid = timeGrid(windows.current.from, windows.current.to, bucketMs);
    const baselineGrid = timeGrid(windows.baseline.from, windows.baseline.to, bucketMs);
    const now =
      seriesOnGrid(currentSeries.buckets, currentGrid, 0)[0]?.values ?? currentGrid.map(() => 0);
    const then =
      seriesOnGrid(baselineSeries.buckets, baselineGrid, 0)[0]?.values ?? baselineGrid.map(() => 0);
    return {
      currentTotal: sum(now),
      baselineTotal: sum(then),
      lines: [
        {
          name: 'This range',
          color: chartColors[0],
          area: true,
          points: now.map(
            (v, i) => [currentGrid[i] - currentGrid[0], v] as [number, number | null],
          ),
        },
        {
          name: kindName.charAt(0).toUpperCase() + kindName.slice(1),
          color: definition.colors.muted,
          dashed: true,
          points: then.map(
            (v, i) => [baselineGrid[i] - baselineGrid[0], v] as [number, number | null],
          ),
        },
      ],
    };
  }, [
    windowsKey,
    currentSeries.buckets,
    baselineSeries.buckets,
    chartColors,
    definition,
    bucketMs,
    isDeploy,
    kindName,
  ]);

  const ratio = chart ? ratioText(chart.currentTotal, chart.baselineTotal) : null;
  const rollout = deploys?.find((d) => Date.parse(d.time) === rolloutMs);

  const deltas =
    currentGroups.buckets && baselineGroups.buckets
      ? deltaRows(
          topRows(currentGroups.buckets, true).map((r) => ({
            key: r.key || '(none)',
            value: r.value,
          })),
          topRows(baselineGroups.buckets, true).map((r) => ({
            key: r.key || '(none)',
            value: r.value,
          })),
          MIN_LINES,
        ).slice(0, DELTA_ROWS)
      : null;
  const deltaMax = deltas ? Math.max(1, ...deltas.flatMap((r) => [r.current, r.baseline])) : 1;
  const deltaError = currentGroups.error ?? baselineGroups.error;
  const seriesError = currentSeries.error ?? baselineSeries.error;

  const curShort = isDeploy ? 'After' : 'Now';
  const cmpShort = isDeploy ? 'Before' : 'Then';
  const loading = (
    <div className="flex h-32 items-center justify-center">
      <Spin indicator={<LoadingOutlined spin />} />
    </div>
  );
  const muted = (text: string) => (
    <div className="flex min-h-20 items-center justify-center px-2 text-center text-sm text-gray-500 dark:text-gray-400">
      {text}
    </div>
  );

  let notReady: string | null = null;
  if (!windows) {
    if (isDeploy) {
      notReady =
        deploys === null
          ? null
          : deploys.length === 0
            ? 'No Kubernetes rollouts in this range: widen it, or compare with another period'
            : 'This rollout is too recent to compare';
    } else notReady = 'Pick when the comparison range starts';
  }

  return (
    <section aria-label="Compare two ranges" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="m-0 text-[14px] font-semibold text-gray-900 dark:text-gray-50">
            Matching lines, compared
          </h3>
          <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {windows
              ? `${isDeploy ? 'After' : 'Now'} against ${kindName} (${span(windows.baseline)})${
                  ratio ? `: lines are ${ratio}${isDeploy ? ' after it' : ''}` : ''
                }`
              : 'Pick what to compare with'}
          </p>
        </div>
        <span className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-gray-500 dark:text-gray-400">Compare with</span>
          <ToggleGroupComponent
            label="Compare with"
            size="small"
            options={KINDS.map((k) => ({
              value: k.value,
              label: k.label,
              pressed: compare === k.value,
            }))}
            onToggle={onCompareChange}
          />
          {compare === 'custom' && (
            <DatePicker
              size="small"
              showTime={{ format: 'HH:mm' }}
              format="YYYY-MM-DD HH:mm"
              placeholder="Compare from…"
              value={customStart}
              onChange={(value) => setCustomStart(value)}
              disabledDate={(d) => d.valueOf() > fromMs}
            />
          )}
        </span>
      </div>

      {isDeploy && deploys === null && loading}
      {isDeploy && rollout && windows && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg bg-gray-50 px-3 py-2 text-[13px] dark:bg-gray-900/60">
          <span
            className="rounded-full px-2 py-0.5 text-xs font-medium"
            style={{
              background: 'color-mix(in srgb, var(--v2-purple) 15%, transparent)',
              color: 'var(--v2-purple)',
            }}
          >
            rollout
          </span>
          <span className="text-gray-800 dark:text-gray-100">
            {rollout.workload} ({rollout.replicaSet}) at {fmt(Date.parse(rollout.time))}
          </span>
          <span className="text-gray-500 dark:text-gray-400">
            · before {span(windows.baseline)} vs after {span(windows.current)}
          </span>
          <span className="flex-1" />
          {deploys && deploys.length > 1 && (
            <>
              <span className="text-xs text-gray-500 dark:text-gray-400">
                From K8s Rollouts; pick another:
              </span>
              {deploys
                .filter((d) => Date.parse(d.time) !== rolloutMs)
                .slice(0, 4)
                .map((d) => (
                  <Button
                    key={`${d.replicaSet}-${d.time}`}
                    size="small"
                    onClick={() => setRolloutMs(Date.parse(d.time))}
                  >
                    {d.workload} · {fmt(Date.parse(d.time))}
                  </Button>
                ))}
            </>
          )}
        </div>
      )}

      {notReady && muted(notReady)}
      {windows && (
        <>
          <div className="flex flex-wrap gap-4 text-xs text-gray-600 dark:text-gray-300">
            {(chart?.lines ?? []).map((line) => (
              <span key={line.name} className="inline-flex items-center gap-1.5">
                <i
                  className="inline-block h-2.5 w-2.5 rounded-sm"
                  style={
                    line.dashed
                      ? { border: `2px dashed ${line.color}`, boxSizing: 'border-box' }
                      : { background: line.color }
                  }
                />
                {line.name}
              </span>
            ))}
          </div>
          {seriesError ? (
            muted(seriesError)
          ) : chart ? (
            <LogsLineChartComponent
              ariaLabel="Matching lines now and in the comparison range"
              series={chart.lines}
              formatValue={(v) => Math.round(v).toLocaleString('en-US')}
              markers={
                isDeploy && rolloutMs !== undefined
                  ? [{ time: rolloutMs, label: 'rollout' }]
                  : undefined
              }
              formatOffset={
                isDeploy
                  ? undefined
                  : (offset) => fmt(windows.current.from + offset, toMs - fromMs > 86_400_000)
              }
              height={200}
            />
          ) : (
            loading
          )}

          <div className="flex flex-col gap-2">
            <div>
              <h3 className="m-0 text-[14px] font-semibold text-gray-900 dark:text-gray-50">
                What changed
              </h3>
              <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                Split by <span className="font-mono">{split}</span> · sorted by absolute change
              </p>
            </div>
            {deltaError ? (
              muted(deltaError)
            ) : !deltas ? (
              loading
            ) : deltas.length === 0 ? (
              muted('No matching lines in either range')
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-[13px]">
                  <thead>
                    <tr className="border-b border-gray-200 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
                      <th className="px-3 py-2 text-left font-mono font-medium">{split}</th>
                      <th className="px-3 py-2 text-right font-medium">{curShort}</th>
                      <th className="px-3 py-2 text-right font-medium">{cmpShort}</th>
                      <th className="px-3 py-2 text-right font-medium">Change</th>
                      <th className="min-w-[160px] px-3 py-2" aria-label="Then and now" />
                    </tr>
                  </thead>
                  <tbody>
                    {deltas.map((row) => (
                      <tr
                        key={row.key}
                        className={`border-b border-gray-100 last:border-0 dark:border-gray-700/60 ${row.small ? 'opacity-60' : ''}`}
                      >
                        <td className="px-3 py-2 font-mono text-gray-800 dark:text-gray-100">
                          {row.key}
                        </td>
                        <td className="px-3 py-2 text-right font-mono">
                          {row.current.toLocaleString('en-US')}
                        </td>
                        <td className="px-3 py-2 text-right font-mono text-gray-500 dark:text-gray-400">
                          {row.baseline.toLocaleString('en-US')}
                        </td>
                        <td className="px-3 py-2 text-right">
                          <ChangePill text={row.change} tone={row.tone} />
                        </td>
                        <td className="px-3 py-2">
                          <span className="flex items-center gap-0.5">
                            <span className="block h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--v2-track)]">
                              <span
                                className="block h-full rounded-full"
                                style={{
                                  width: `${(row.baseline / deltaMax) * 100}%`,
                                  background: definition.colors.muted,
                                }}
                              />
                            </span>
                            <span className="block h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--v2-track)]">
                              <span
                                className="block h-full rounded-full"
                                style={{
                                  width: `${(row.current / deltaMax) * 100}%`,
                                  background: chartColors[0],
                                }}
                              />
                            </span>
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <div>
              <h3 className="m-0 text-[14px] font-semibold text-gray-900 dark:text-gray-50">
                Patterns that changed
              </h3>
              <p className="m-0 mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                Message patterns new, spiking or dropping against the comparison range; click one to
                search it
              </p>
            </div>
            {patterns === 'error' ? (
              muted('Pattern changes are not available for this search')
            ) : patterns === null ? (
              loading
            ) : patterns.changes.length === 0 ? (
              muted('No pattern changed much')
            ) : (
              <div className="flex flex-col divide-y divide-gray-100 dark:divide-gray-700/60">
                {patterns.changes.map((change) => (
                  <button
                    key={change.hash}
                    type="button"
                    onClick={() => onFilterPattern(change.hash)}
                    className="flex cursor-pointer items-center gap-3 border-0 bg-transparent px-1 py-2 text-left text-[13px]"
                  >
                    <Tooltip title={change.sample}>
                      <span className="min-w-0 flex-1 truncate font-mono text-gray-800 dark:text-gray-100">
                        {change.pattern}
                      </span>
                    </Tooltip>
                    <span className="w-20 text-right font-mono">
                      {change.current.toLocaleString('en-US')}
                    </span>
                    <span className="w-20 text-right font-mono text-gray-500 dark:text-gray-400">
                      {change.baseline.toLocaleString('en-US')}
                    </span>
                    <span
                      className="w-16 text-right text-xs font-medium"
                      style={{ color: change.change === 'drop' ? status.ok : status.bad }}
                    >
                      {change.change}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </>
      )}
      <span className="text-xs text-gray-500 dark:text-gray-400">
        The comparison range is the same length as this one. Changes under {MIN_LINES} lines are
        greyed out.
      </span>
    </section>
  );
};
