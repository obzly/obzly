import type { JSX } from 'react';

import type { Panel } from '../../../entity/dashboards';
import { type StatusColors, useTheme } from '../../../shared/theme';
import {
  ChangePill,
  Donut,
  Gauge,
  RankedList,
  Sparkline,
  StatusStrip,
  type StripState,
} from '../../../shared/ui/v2';
import {
  type ChangeText,
  type StackedSummary,
  type StatusRow,
  foldIntoOther,
  gaugeRatio,
  thresholdState,
} from '../lib/panelKinds';
import { formatByUnit, shortLegendName } from './echartsOption';

// Renderers for the v2 panel kinds. Each takes data already reduced by
// lib/panelKinds; none queries.

// Threshold states are status colours (green stays green in every theme); "none"
// (no thresholds set) is just the accent.
const stateColor = (status: StatusColors, state: StripState): string =>
  ({ ok: status.ok, warn: status.warn, fail: status.bad, none: 'var(--v2-accent)' })[state];

const Empty = ({ children }: { children: string }): JSX.Element => (
  <div className="flex h-full items-center justify-center px-2 text-center text-sm text-gray-500 dark:text-gray-400">
    {children}
  </div>
);

export const StatTrendView = ({
  panel,
  value,
  change,
  window,
  trend,
}: {
  panel: Panel;
  value: number | null;
  // Undefined while the previous window is still loading.
  change: ChangeText | undefined;
  window: string;
  trend: number[];
}): JSX.Element => (
  <div className="flex h-full flex-col justify-center gap-2 overflow-hidden px-2">
    <div className="text-4xl leading-none font-semibold text-gray-900 dark:text-gray-50">
      {value === null ? 'n/a' : formatByUnit(value, panel.unit)}
    </div>
    {change && (
      <div className="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
        <ChangePill text={change.text} tone={change.tone} />
        <span>vs previous {window}</span>
      </div>
    )}
    {trend.length > 1 && (
      <Sparkline
        values={trend}
        color="var(--v2-accent)"
        height={40}
        label={`${panel.title} trend`}
      />
    )}
  </div>
);

export const RankedListView = ({
  panel,
  rows,
}: {
  panel: Panel;
  rows: { key: string; value: number }[];
}): JSX.Element => (
  <div className="h-full overflow-auto px-1 py-1">
    <RankedList
      rows={rows.slice(0, panel.limit ?? 10).map((row) => ({
        key: row.key,
        label: (
          <span className="font-mono text-xs" title={row.key}>
            {shortLegendName(row.key)}
          </span>
        ),
        value: row.value,
        display: formatByUnit(row.value, panel.unit),
      }))}
      empty="No data in this range"
    />
  </div>
);

export const DonutView = ({
  panel,
  rows,
  colors,
}: {
  panel: Panel;
  rows: { key: string; value: number }[];
  colors: string[];
}): JSX.Element => {
  if (rows.length === 0) return <Empty>No data in this range</Empty>;
  const slices = foldIntoOther(rows);
  const total = slices.reduce((sum, slice) => sum + slice.value, 0);
  const parts = slices.map((slice, i) => ({
    key: slice.key,
    value: slice.value,
    color: colors[i % colors.length],
  }));
  return (
    <div className="flex h-full flex-wrap items-center justify-center gap-5 overflow-auto px-1">
      <Donut
        parts={parts}
        size={132}
        center={formatByUnit(total, panel.unit)}
        centerSub="total"
        label={`${panel.title}: ${parts.map((p) => `${p.key} ${formatByUnit(p.value, panel.unit)}`).join(', ')}`}
      />
      <div className="flex min-w-[140px] flex-1 flex-col gap-2">
        {parts.map((part) => (
          <span key={part.key} className="flex items-center gap-2 text-sm">
            <span className="size-2.5 shrink-0 rounded-[3px]" style={{ background: part.color }} />
            <span
              className="min-w-0 flex-1 truncate text-gray-800 dark:text-gray-100"
              title={part.key}
            >
              {shortLegendName(part.key)}
            </span>
            <strong className="font-semibold text-gray-900 dark:text-gray-50">
              {formatByUnit(part.value, panel.unit)}
            </strong>
            <span className="w-12 text-right text-gray-500 dark:text-gray-400">
              {total > 0 ? `${((part.value / total) * 100).toFixed(1)}%` : 'n/a'}
            </span>
          </span>
        ))}
      </div>
    </div>
  );
};

const MAX_GAUGES = 3;

export const GaugeView = ({
  panel,
  gauges,
}: {
  panel: Panel;
  // One per group, or a single unlabelled one; value null = no data.
  gauges: { key: string; value: number | null }[];
}): JSX.Element => {
  const { status } = useTheme();
  const shown = gauges.slice(0, MAX_GAUGES);
  const hasMax = panel.max !== undefined || panel.unit === 'percent';
  const caption = hasMax
    ? `of ${formatByUnit(panel.max ?? 1, panel.unit)}`
    : 'set a maximum in the panel settings';
  return (
    <div className="flex h-full flex-wrap items-center justify-around gap-3 overflow-auto">
      {shown.map((gauge) => {
        const state =
          gauge.value === null
            ? null
            : thresholdState(gauge.value, panel.thresholds, panel.higherIs);
        return (
          <span key={gauge.key} className="flex flex-col items-center gap-1">
            <Gauge
              ratio={
                gauge.value === null
                  ? null
                  : gaugeRatio(gauge.value, panel.min, panel.max, panel.unit)
              }
              value={gauge.value === null ? 'n/a' : formatByUnit(gauge.value, panel.unit)}
              caption={caption}
              color={state ? stateColor(status, state) : undefined}
              size={shown.length > 1 ? 112 : 140}
              label={`${panel.title}${gauge.key ? ` ${gauge.key}` : ''}`}
            />
            {gauge.key && (
              <span
                className="max-w-[140px] truncate font-mono text-xs text-gray-500 dark:text-gray-400"
                title={gauge.key}
              >
                {shortLegendName(gauge.key)}
              </span>
            )}
          </span>
        );
      })}
      {gauges.length > MAX_GAUGES && (
        <span className="w-full text-center text-xs text-gray-500 dark:text-gray-400">
          showing the top {MAX_GAUGES} of {gauges.length}
        </span>
      )}
    </div>
  );
};

export const StackedFooter = ({
  panel,
  summary,
  colors,
}: {
  panel: Panel;
  summary: StackedSummary;
  colors: string[];
}): JSX.Element => {
  const legend = [...summary.series].sort((a, b) => b.total - a.total);
  const shown = legend.slice(0, 6);
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-t border-gray-200 pt-2 text-[13px] dark:border-gray-700">
      <span>
        <span className="text-gray-500 dark:text-gray-400">Peak</span>{' '}
        <strong className="font-semibold text-gray-900 dark:text-gray-50">
          {summary.peak
            ? `${new Date(summary.peak.timestamp).toLocaleString()} · ${formatByUnit(summary.peak.total, panel.unit)}`
            : 'n/a'}
        </strong>
      </span>
      {summary.total !== null && (
        <span>
          <span className="text-gray-500 dark:text-gray-400">Total</span>{' '}
          <strong className="font-semibold text-gray-900 dark:text-gray-50">
            {formatByUnit(summary.total, panel.unit)}
          </strong>
        </span>
      )}
      {(legend.length > 1 || legend[0]?.key) &&
        shown.map((series) => (
          <span
            key={series.key}
            className="flex items-center gap-1 text-gray-500 dark:text-gray-400"
            title={series.key}
          >
            <span
              className="size-2 rounded-[2px]"
              style={{ background: colors[series.colorIndex % colors.length] }}
            />
            {shortLegendName(series.key || '(none)')}
            {series.share !== null && ` ${(series.share * 100).toFixed(0)}%`}
          </span>
        ))}
      {legend.length > shown.length && (
        <span className="text-gray-500 dark:text-gray-400">
          +{legend.length - shown.length} more
        </span>
      )}
    </div>
  );
};

export const StatusStripView = ({
  panel,
  rows,
}: {
  panel: Panel;
  rows: StatusRow[];
}): JSX.Element => {
  if (!panel.thresholds) {
    return <Empty>Set a warn or critical threshold in the panel settings</Empty>;
  }
  if (rows.length === 0) return <Empty>No data in this range</Empty>;
  return (
    <div className="flex h-full flex-col gap-2.5 overflow-auto px-1 py-1">
      {rows.map((row) => (
        <span key={row.key} className="flex flex-col gap-1">
          <span className="flex justify-between gap-2 text-xs">
            <span
              className="min-w-0 truncate font-mono text-gray-800 dark:text-gray-100"
              title={row.key}
            >
              {shortLegendName(row.key)}
            </span>
            <span className="shrink-0 text-gray-500 dark:text-gray-400">
              {row.latest === null ? 'no data' : formatByUnit(row.latest, panel.unit)}
            </span>
          </span>
          <StatusStrip cells={row.cells} height={12} label={`${row.key} status per time bucket`} />
        </span>
      ))}
    </div>
  );
};
