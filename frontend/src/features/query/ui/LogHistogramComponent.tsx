import { DiffOutlined, FilterOutlined, ZoomOutOutlined } from '@ant-design/icons';
import { Badge, Button, Checkbox, Popover, Select, Spin, Switch, Tooltip } from 'antd';
import type { ECharts } from 'echarts/core';
import React, { useEffect, useMemo, useRef, useState } from 'react';

import { dashboardApi } from '../../../entity/dashboards';
import { type DeployMarker, type QueryNode, queryApi } from '../../../entity/query';
import { useTheme } from '../../../shared/theme';
import {
  formatInTimezone,
  timezoneShiftMs,
  unshiftTimezone,
  useDisplayTimezone,
} from '../../../shared/time';
import { computeAutoBucketMs, echarts } from '../../dashboards';
import { mergeBuckets } from '../../dashboards/lib/mergeBuckets';
import { levelColor, sortLevels } from '../lib/logLevels';
import type { TimeRange } from './TimeRangePickerComponent';

interface Props {
  projectId: string;
  /**
   * The search without the severity filter (hidden levels come via
   * `excludedLevels`), so toggling a level reuses the cached aggregation.
   */
  query: QueryNode | null;
  excludedLevels: ReadonlySet<string>;
  timeRange: TimeRange;
  refreshToken: number;
  /** ISO timestamp to mark with a vertical line - the hovered/selected log row. */
  cursorTime?: string | null;
  /** Clicking a bar's column zooms the search to that bucket's time span. */
  onZoom?: (range: { from: number; to: number }) => void;
  /** Shown as a "Zoom out" button when set - undoes the last zoom. */
  onZoomOut?: () => void;
  /** Matching logs summed from the buckets (the search page doesn't count); null on failure. */
  onTotal?: (total: number | null) => void;
  /** Logs per level over the whole range, hidden levels included. null on failure. */
  onLevelCounts?: (counts: Record<string, number> | null) => void;
  /** Shows an "Anomalies" button. */
  onAnomalies?: () => void;
}

interface Bucket {
  timestamp?: string;
  groupKey?: string;
  value: number;
}

const BUCKET_STORAGE_KEY = 'obzly-log-histogram-bucket';
const DEPLOYS_STORAGE_KEY = 'obzly-log-histogram-deploys';
// More deploy markers than this show their names in the bar tooltip only.
const MAX_LABELLED_DEPLOYS = 6;
// Shared with the drag-selection overlay, which must line up with the plot.
const GRID = { left: 44, right: 12, top: 8, bottom: 22 };
// Pointer travel (px) below which a press counts as a click, not a drag.
const DRAG_THRESHOLD_PX = 4;
// The backend caps an aggregation at 10,000 rows, one per (bucket, level):
// 500 bars x ~10 levels fits.
const MAX_BUCKETS = 500;
const AGGREGATION_LIMIT = 10_000;

const BUCKET_OPTIONS: { label: string; ms: number }[] = [
  { label: '10s', ms: 10_000 },
  { label: '30s', ms: 30_000 },
  { label: '1m', ms: 60_000 },
  { label: '5m', ms: 5 * 60_000 },
  { label: '15m', ms: 15 * 60_000 },
  { label: '1h', ms: 3_600_000 },
  { label: '6h', ms: 6 * 3_600_000 },
  { label: '1d', ms: 86_400_000 },
];

const readStoredBucket = (): number | 'auto' => {
  try {
    const stored = localStorage.getItem(BUCKET_STORAGE_KEY);
    const parsed = stored ? Number(stored) : NaN;
    return BUCKET_OPTIONS.some((o) => o.ms === parsed) ? parsed : 'auto';
  } catch {
    return 'auto';
  }
};

// Level names are ingested data (an unrecognized raw level is stored
// as-is), and echarts renders a tooltip formatter's string as HTML.
const escapeHtml = (value: string): string =>
  value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');

const formatBucketLabel = (ms: number): string =>
  BUCKET_OPTIONS.find((o) => o.ms === ms)?.label ?? `${Math.round(ms / 1000)}s`;

/** Log volume stacked by level. Its level filter affects the chart only, not the search. */
export const LogHistogramComponent = ({
  projectId,
  query,
  excludedLevels,
  timeRange,
  refreshToken,
  cursorTime,
  onZoom,
  onZoomOut,
  onTotal,
  onLevelCounts,
  onAnomalies,
}: Props): React.JSX.Element => {
  const { theme } = useTheme();
  const isDark = theme === 'dark';
  // ECharts renders local time or UTC only: values are shifted so UTC
  // rendering shows the display timezone's wall clock.
  const [timezone] = useDisplayTimezone();
  const shift = useMemo(() => (ms: number) => ms + timezoneShiftMs(ms, timezone), [timezone]);
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);
  // A ref so a new callback identity doesn't force a chart rebuild.
  const onZoomRef = useRef(onZoom);
  useEffect(() => {
    onZoomRef.current = onZoom;
  }, [onZoom]);

  const [bucketChoice, setBucketChoice] = useState<number | 'auto'>(readStoredBucket);
  const [allBuckets, setAllBuckets] = useState<Bucket[]>([]);
  const [deploys, setDeploys] = useState<DeployMarker[]>([]);
  // Read by the tooltip formatter, which is built with the chart.
  const deploysRef = useRef<DeployMarker[]>([]);
  useEffect(() => {
    deploysRef.current = deploys;
  }, [deploys]);
  const [showDeploys, setShowDeploys] = useState<boolean>(() => {
    try {
      return localStorage.getItem(DEPLOYS_STORAGE_KEY) === 'true'; // off until turned on
    } catch {
      return false;
    }
  });
  const buckets = useMemo(
    () => allBuckets.filter((b) => !excludedLevels.has(b.groupKey || 'UNKNOWN')),
    [allBuckets, excludedLevels],
  );
  // Refs, so a new callback identity doesn't refetch the histogram.
  const onTotalRef = useRef(onTotal);
  const onLevelCountsRef = useRef(onLevelCounts);
  const excludedLevelsRef = useRef(excludedLevels);
  useEffect(() => {
    onTotalRef.current = onTotal;
    onLevelCountsRef.current = onLevelCounts;
    excludedLevelsRef.current = excludedLevels;
  });
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [chartHiddenLevels, setChartHiddenLevels] = useState<Set<string>>(new Set());
  // The drag band in container pixels, plus its times for the label.
  const [dragBox, setDragBox] = useState<{
    left: number;
    right: number;
    from: number;
    to: number;
  } | null>(null);

  const rangeMs = Math.max(timeRange.to.valueOf() - timeRange.from.valueOf(), 1);
  const isBucketUsable = (ms: number) => ms <= rangeMs && rangeMs / ms <= MAX_BUCKETS;
  // A stored width that doesn't fit this range uses auto, keeping the choice.
  const bucketMs =
    bucketChoice !== 'auto' && isBucketUsable(bucketChoice)
      ? bucketChoice
      : computeAutoBucketMs(timeRange.from.valueOf(), timeRange.to.valueOf());

  const handleBucketChange = (value: number | 'auto') => {
    setBucketChoice(value);
    try {
      if (value === 'auto') localStorage.removeItem(BUCKET_STORAGE_KEY);
      else localStorage.setItem(BUCKET_STORAGE_KEY, String(value));
    } catch {
      /* storage unavailable - the choice just won't persist */
    }
  };

  useEffect(() => {
    let cancelled = false;
    // Aborting also stops the query on the Site when the search changes.
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);

    // Bars fill in as partitions stream; totals are reported at the end.
    const totals = new Map<string, Bucket>();
    let merged: Bucket[] = [];
    setAllBuckets([]);
    dashboardApi
      .streamPanelQuery(
        projectId,
        {
          panel: {
            id: 'log-search-histogram',
            title: '',
            type: 'bar',
            source: 'logs',
            query: query ?? undefined,
            groupField: 'level',
            bucketMs,
            limit: AGGREGATION_LIMIT,
            gridPos: { x: 0, y: 0, w: 1, h: 1 },
          },
          timeRange: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
        },
        (buckets) => {
          if (cancelled) return;
          merged = mergeBuckets(totals, buckets);
          setAllBuckets(merged);
        },
        abort.signal,
      )
      .then(() => {
        if (cancelled) return;
        const counts: Record<string, number> = {};
        for (const b of merged) {
          const level = b.groupKey || 'UNKNOWN';
          counts[level] = (counts[level] ?? 0) + b.value;
        }
        onLevelCountsRef.current?.(counts);
        onTotalRef.current?.(
          Object.entries(counts)
            .filter(([level]) => !excludedLevelsRef.current.has(level))
            .reduce((sum, [, count]) => sum + count, 0),
        );
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        setError((e as Error).message);
        onTotalRef.current?.(null);
        onLevelCountsRef.current?.(null);
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });

    return () => {
      cancelled = true;
      abort.abort();
    };
  }, [projectId, query, timeRange, bucketMs, refreshToken]);

  const levels = useMemo(() => sortLevels(buckets.map((b) => b.groupKey || 'UNKNOWN')), [buckets]);
  const total = useMemo(() => buckets.reduce((sum, b) => sum + b.value, 0), [buckets]);

  // The cursor line is merged separately so hovering rows doesn't rebuild this.
  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;

    const text = isDark ? '#e5e7eb' : '#374151';
    const axisLine = isDark ? '#4b5563' : '#d1d5db';
    const splitLine = isDark ? '#374151' : '#e5e7eb';
    const half = bucketMs / 2;

    // ECharts stacks by data index, not x value, so every level series
    // needs a (zero-filled) point for every bucket.
    const bucketTimes = [
      ...new Set(
        buckets.filter((b) => b.timestamp).map((b) => new Date(b.timestamp as string).valueOf()),
      ),
    ].sort((a, b) => a - b);
    const countsByLevel = new Map<string, Map<number, number>>();
    for (const b of buckets) {
      if (!b.timestamp) continue;
      const level = b.groupKey || 'UNKNOWN';
      if (!countsByLevel.has(level)) countsByLevel.set(level, new Map());
      countsByLevel.get(level)?.set(new Date(b.timestamp).valueOf(), b.value);
    }

    // Explicit bar width: ECharts infers it from point spacing, so a lone
    // bucket rendered far too wide.
    const visibleLevels = levels.filter((level) => !chartHiddenLevels.has(level));
    const barWidth = () => {
      const plotWidth = Math.max(chart.getWidth() - GRID.left - GRID.right, 1);
      const rangeMs = Math.max(timeRange.to.valueOf() - timeRange.from.valueOf(), 1);
      return Math.max(1, ((plotWidth * bucketMs) / rangeMs) * 0.85);
    };

    const levelSeries = visibleLevels.map((level) => ({
      id: `level-${level}`,
      name: level,
      type: 'bar' as const,
      stack: 'volume',
      barWidth: barWidth(),
      itemStyle: { color: levelColor(level) },
      emphasis: { focus: 'series' as const },
      // Centered on the bucket, so a bar spans the time it counts.
      data: bucketTimes.map((ts) => [shift(ts + half), countsByLevel.get(level)?.get(ts) ?? 0]),
    }));

    chart.setOption(
      {
        animation: false,
        useUTC: true,
        grid: GRID,
        tooltip: {
          trigger: 'axis',
          axisPointer: { type: 'shadow' },
          backgroundColor: isDark ? '#1f2937' : '#ffffff',
          borderColor: axisLine,
          textStyle: { color: text, fontSize: 12 },
          formatter: (params: unknown) => {
            const items = (Array.isArray(params) ? params : [params]) as {
              seriesName: string;
              value: [number, number];
              color: string;
            }[];
            if (items.length === 0) return '';
            const startMs = unshiftTimezone(items[0].value[0], timezone) - half;
            const lines = items
              .filter((i) => i.value[1] > 0)
              .map(
                (i) =>
                  `<span style="display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:6px;background:${i.color}"></span>${escapeHtml(i.seriesName)}: <b>${i.value[1].toLocaleString()}</b>`,
              );
            // Deploy markers are silent, so the bar tooltip names them.
            for (const d of deploysRef.current) {
              const at = new Date(d.time).valueOf();
              if (at >= startMs && at < startMs + bucketMs) {
                lines.push(
                  `<span style="color:#f59e0b">&#9670;</span> ${escapeHtml(d.workload)} rolled out (${escapeHtml(d.namespace)}, ${d.pods} pod${d.pods === 1 ? '' : 's'})`,
                );
              }
            }
            const startText = formatInTimezone(startMs, 'YYYY-MM-DD HH:mm:ss', timezone);
            return [`${startText} (+${formatBucketLabel(bucketMs)})`, ...lines].join('<br/>');
          },
        },
        xAxis: {
          type: 'time',
          min: shift(timeRange.from.valueOf()),
          max: shift(timeRange.to.valueOf()),
          axisLine: { lineStyle: { color: axisLine } },
          axisLabel: { color: text, fontSize: 11, hideOverlap: true },
          splitLine: { show: false },
        },
        yAxis: {
          type: 'value',
          minInterval: 1,
          splitNumber: 2,
          axisLabel: {
            color: text,
            fontSize: 11,
            formatter: (v: number) => Intl.NumberFormat('en', { notation: 'compact' }).format(v),
          },
          splitLine: { lineStyle: { color: splitLine } },
        },
        series: [
          ...levelSeries,
          // Own series so the line survives every level being filtered out.
          { id: 'cursor', type: 'line', data: [], silent: true, symbol: 'none' },
          // Carries the deploy markers, likewise.
          { id: 'deploys', type: 'line', data: [], silent: true, symbol: 'none' },
        ],
      },
      true,
    );

    const handleResize = () => {
      chart.resize();
      const width = barWidth();
      chart.setOption({
        series: visibleLevels.map((level) => ({ id: `level-${level}`, barWidth: width })),
      });
    };
    window.addEventListener('resize', handleResize);

    // A click zooms to the whole bucket column (short bars are hard to hit);
    // a drag zooms to the selected span. Pointer capture keeps tracking
    // outside the chart.
    const dom = chart.getDom();
    const zr = chart.getZr();
    const local = (e: PointerEvent) => {
      const rect = dom.getBoundingClientRect();
      return { x: e.clientX - rect.left, y: e.clientY - rect.top };
    };
    const inPlot = (x: number, y: number) => chart.containPixel('grid', [x, y]);
    const clampX = (x: number) => Math.min(Math.max(x, GRID.left), dom.clientWidth - GRID.right);
    const timeAt = (x: number) =>
      unshiftTimezone(Number(chart.convertFromPixel({ xAxisIndex: 0 }, x)), timezone);
    const clampRange = (from: number, to: number) => ({
      from: Math.max(from, timeRange.from.valueOf()),
      to: Math.min(to, timeRange.to.valueOf()),
    });

    let dragStartX: number | null = null;
    let dragging = false;

    const endGesture = (e?: PointerEvent) => {
      if (e && dom.hasPointerCapture(e.pointerId)) dom.releasePointerCapture(e.pointerId);
      dragStartX = null;
      dragging = false;
      setDragBox(null);
    };

    const handlePointerDown = (e: PointerEvent) => {
      if (e.button !== 0 || !onZoomRef.current) return;
      const { x, y } = local(e);
      if (!inPlot(x, y)) return;
      dragStartX = x;
      dragging = false;
      dom.setPointerCapture(e.pointerId);
    };

    const handlePointerMove = (e: PointerEvent) => {
      const { x, y } = local(e);
      if (dragStartX === null) {
        zr.setCursorStyle(onZoomRef.current && inPlot(x, y) ? 'zoom-in' : 'default');
        return;
      }
      const current = clampX(x);
      if (!dragging && Math.abs(current - dragStartX) >= DRAG_THRESHOLD_PX) {
        dragging = true;
        chart.dispatchAction({ type: 'hideTip' });
      }
      if (!dragging) return;
      zr.setCursorStyle('ew-resize');
      const left = Math.min(dragStartX, current);
      const right = Math.max(dragStartX, current);
      setDragBox({ left, right, ...clampRange(timeAt(left), timeAt(right)) });
    };

    const handlePointerUp = (e: PointerEvent) => {
      const zoom = onZoomRef.current;
      if (dragStartX === null || !zoom) {
        endGesture(e);
        return;
      }
      let range: { from: number; to: number } | null = null;
      if (dragging) {
        const current = clampX(local(e).x);
        range = clampRange(
          timeAt(Math.min(dragStartX, current)),
          timeAt(Math.max(dragStartX, current)),
        );
      } else {
        const ts = timeAt(dragStartX);
        if (Number.isFinite(ts)) {
          const start = Math.floor(ts / bucketMs) * bucketMs;
          range = clampRange(start, start + bucketMs);
        }
      }
      endGesture(e);
      if (range && Number.isFinite(range.from) && range.to > range.from) {
        zoom({ from: Math.round(range.from), to: Math.round(range.to) });
      }
    };

    const handlePointerCancel = (e: PointerEvent) => endGesture(e);

    dom.addEventListener('pointerdown', handlePointerDown);
    dom.addEventListener('pointermove', handlePointerMove);
    dom.addEventListener('pointerup', handlePointerUp);
    dom.addEventListener('pointercancel', handlePointerCancel);

    return () => {
      window.removeEventListener('resize', handleResize);
      dom.removeEventListener('pointerdown', handlePointerDown);
      dom.removeEventListener('pointermove', handlePointerMove);
      dom.removeEventListener('pointerup', handlePointerUp);
      dom.removeEventListener('pointercancel', handlePointerCancel);
      setDragBox(null);
    };
  }, [buckets, levels, chartHiddenLevels, isDark, bucketMs, timeRange, timezone, shift]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const ts = cursorTime ? new Date(cursorTime).valueOf() : NaN;
    chart.setOption({
      series: [
        {
          id: 'cursor',
          markLine: {
            silent: true,
            symbol: 'none',
            animation: false,
            label: { show: false },
            lineStyle: { color: '#10b981', width: 1.5, type: 'solid' },
            data: Number.isFinite(ts) ? [{ xAxis: shift(ts) }] : [],
          },
        },
      ],
    });
  }, [cursorTime, buckets, levels, chartHiddenLevels, isDark, shift]);

  // Deploy markers are silent: hovering one would replace the bar tooltip
  // and zoom cursor.
  useEffect(() => {
    if (!showDeploys) {
      setDeploys([]);
      return;
    }
    const abort = new AbortController();
    queryApi
      .getDeploys(
        projectId,
        {
          query: query ?? undefined,
          timeRange: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
        },
        abort.signal,
      )
      .then((response) => setDeploys(response.markers))
      .catch(() => setDeploys([])); // an older Site, or a failure: no markers
    return () => abort.abort();
  }, [projectId, query, timeRange, refreshToken, showDeploys]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    // Labels only while they fit; every marker names itself on hover.
    const labelled = deploys.length <= MAX_LABELLED_DEPLOYS;
    chart.setOption({
      series: [
        {
          id: 'deploys',
          markLine: {
            silent: true,
            symbol: 'none',
            animation: false,
            lineStyle: { color: '#f59e0b', width: 1, type: 'dashed' },
            label: {
              show: labelled,
              position: 'insideEndTop',
              fontSize: 9,
              color: isDark ? '#fbbf24' : '#b45309',
              formatter: (p: { data?: { name?: string } }) => p.data?.name ?? '',
            },
            data: deploys.map((d) => ({
              xAxis: shift(new Date(d.time).valueOf()),
              name: `${d.workload} rolled out (${d.namespace}, ${d.pods} pod${d.pods === 1 ? '' : 's'})`,
            })),
          },
        },
      ],
    });
  }, [deploys, buckets, levels, chartHiddenLevels, isDark, shift]);

  const toggleDeploys = (on: boolean) => {
    setShowDeploys(on);
    try {
      localStorage.setItem(DEPLOYS_STORAGE_KEY, String(on));
    } catch {
      /* storage unavailable - the choice just won't persist */
    }
  };

  useEffect(() => {
    return () => {
      chartRef.current?.dispose();
      chartRef.current = null;
    };
  }, []);

  const filterContent = (
    <div className="flex min-w-36 flex-col gap-1">
      {levels.length === 0 ? (
        <span className="text-xs text-gray-400">No levels in range</span>
      ) : (
        levels.map((level) => (
          <Checkbox
            key={level}
            checked={!chartHiddenLevels.has(level)}
            onChange={(e) =>
              setChartHiddenLevels((prev) => {
                const next = new Set(prev);
                if (e.target.checked) next.delete(level);
                else next.add(level);
                return next;
              })
            }
          >
            <span className="inline-flex items-center gap-1.5 text-xs">
              <span className="h-2 w-2 rounded-sm" style={{ background: levelColor(level) }} />
              {level}
            </span>
          </Checkbox>
        ))
      )}
      {chartHiddenLevels.size > 0 && (
        <Button size="small" type="link" onClick={() => setChartHiddenLevels(new Set())}>
          Show all
        </Button>
      )}
    </div>
  );

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between px-1 pb-1">
        <span className="text-xs text-gray-500 dark:text-gray-300">
          {isLoading ? (
            <span className="inline-flex items-center gap-1">
              {total > 0 && `${total.toLocaleString()} logs so far`}
              <Spin size="small" />
            </span>
          ) : (
            `${total.toLocaleString()} logs`
          )}
        </span>
        <div className="flex items-center gap-2">
          {onZoom && (
            <span className="hidden text-[11px] text-gray-400 md:inline dark:text-gray-400">
              Click a bar or drag to zoom
            </span>
          )}
          {onZoomOut && (
            <Button size="small" icon={<ZoomOutOutlined />} onClick={onZoomOut}>
              Zoom out
            </Button>
          )}
          {onAnomalies && (
            <Tooltip title="Message patterns that are new, spiking or dropping against an earlier period">
              <Button size="small" icon={<DiffOutlined />} onClick={onAnomalies}>
                Anomalies
              </Button>
            </Tooltip>
          )}
          <Tooltip title="Mark Kubernetes rollouts on the chart: when a Deployment's new ReplicaSet first logged">
            <span className="inline-flex items-center gap-1 text-[11px] text-gray-500 dark:text-gray-300">
              <Switch size="small" checked={showDeploys} onChange={toggleDeploys} />
              K8s Rollouts
            </span>
          </Tooltip>
          <Popover content={filterContent} trigger="click" placement="bottomRight">
            <Badge dot={chartHiddenLevels.size > 0} offset={[-2, 2]}>
              <Button
                size="small"
                icon={<FilterOutlined />}
                title="Choose which levels the chart shows (the results aren't affected)"
              >
                Filter
              </Button>
            </Badge>
          </Popover>
          <Select
            size="small"
            value={bucketChoice !== 'auto' && isBucketUsable(bucketChoice) ? bucketChoice : 'auto'}
            onChange={handleBucketChange}
            style={{ width: 96 }}
            popupMatchSelectWidth={false}
            options={[
              { value: 'auto', label: `Auto (${formatBucketLabel(bucketMs)})` },
              ...BUCKET_OPTIONS.map((o) => ({
                value: o.ms,
                label: o.label,
                disabled: !isBucketUsable(o.ms),
              })),
            ]}
          />
        </div>
      </div>
      {/* Stays mounted on error: the echarts instance is bound to this element. */}
      <div className="relative min-h-0 w-full flex-1">
        {/* touch-none: a drag selects a range instead of scrolling. */}
        <div ref={containerRef} className="h-full w-full touch-none select-none" />
        {dragBox && (
          <div
            className="pointer-events-none absolute border-x border-emerald-500 bg-emerald-500/15"
            style={{
              left: dragBox.left,
              width: dragBox.right - dragBox.left,
              top: GRID.top,
              bottom: GRID.bottom,
            }}
          >
            <span className="absolute -top-0.5 left-1/2 -translate-x-1/2 rounded bg-emerald-600 px-1 text-[10px] whitespace-nowrap text-white">
              {formatInTimezone(dragBox.from, 'HH:mm:ss', timezone)} –{' '}
              {formatInTimezone(dragBox.to, 'HH:mm:ss', timezone)}
            </span>
          </div>
        )}
        {error && (
          <div className="absolute inset-0 flex items-center justify-center bg-white/80 px-2 text-center text-sm text-red-500 dark:bg-gray-800/80">
            {error}
          </div>
        )}
      </div>
    </div>
  );
};
