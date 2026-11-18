import { Segmented } from 'antd';
import type { JSX } from 'react';
import { useMemo, useState } from 'react';

import type { WebAnalyticsBucket, WebAnalyticsRow } from '../../../entity/webAnalytics';
import { downloadText, toCSV } from '../../../shared/lib';
import type { StatusColors } from '../../../shared/theme';
import { Card, Donut } from '../../../shared/ui/v2';
import { statusColor, withAlpha } from '../lib/colors';
import { axisLabels, bucketLabel, formatBytes, formatCount, formatShare } from '../lib/format';
import { buildHeatmap } from '../lib/heatmap';
import { PanelFrame } from './panels';

export type Measure = 'req' | 'vis' | 'bw';

interface ChartProps {
  series: WebAnalyticsBucket[];
  bucketSeconds: number;
  rangeText: string;
  status: StatusColors;
  visitorsColor: string;
  bandwidthColor: string;
  textColor: string;
  visitorsKnown: boolean;
}

/** Requests over time: stacked by status class, or visitors / bandwidth per bucket; the live part hatched. */
export const RequestsChart = ({
  series,
  bucketSeconds,
  rangeText,
  status,
  visitorsColor,
  bandwidthColor,
  textColor,
  visitorsKnown,
}: ChartProps): JSX.Element => {
  const [measure, setMeasure] = useState<Measure>('req');
  const segments = (
    bucket: WebAnalyticsBucket,
  ): { value: number; color: string; label: string }[] => {
    if (measure === 'vis')
      return [{ value: bucket.visitors, color: visitorsColor, label: 'Visitors' }];
    if (measure === 'bw')
      return [{ value: bucket.bytes, color: bandwidthColor, label: 'Bandwidth' }];
    return [
      { value: bucket.status2xx, color: status.ok, label: '2xx' },
      { value: bucket.status3xx, color: status.info, label: '3xx' },
      { value: bucket.status4xx, color: status.warn, label: '4xx' },
      { value: bucket.status5xx, color: status.bad, label: '5xx' },
    ];
  };
  const totals = series.map((b) => segments(b).reduce((sum, s) => sum + s.value, 0));
  const max = Math.max(1, ...totals);
  const peakIndex = totals.indexOf(Math.max(...totals, 0));
  const format = measure === 'bw' ? formatBytes : formatCount;
  const legend =
    measure === 'req'
      ? [
          { label: '2xx', color: status.ok },
          { label: '3xx', color: status.info },
          { label: '4xx', color: status.warn },
          { label: '5xx', color: status.bad },
        ]
      : [
          {
            label: measure === 'vis' ? 'Visitors' : 'Bandwidth',
            color: measure === 'vis' ? visitorsColor : bandwidthColor,
          },
        ];
  const labels = axisLabels(
    series.map((b) => b.start),
    bucketSeconds,
  );
  const hatch = `repeating-linear-gradient(45deg, transparent 0 3px, ${withAlpha(textColor, 0.16)} 3px 5px)`;
  const hasLive = series.some((b) => b.live);

  return (
    <Card
      title="Requests over time"
      subtitle={
        measure === 'req'
          ? 'Stacked by status class'
          : measure === 'vis'
            ? 'Distinct visitors per bucket (crawlers left out when set)'
            : 'Response body bytes per bucket'
      }
      extra={
        <Segmented
          size="small"
          aria-label="Measure"
          value={measure}
          onChange={(value) => setMeasure(value as Measure)}
          options={[
            { value: 'req', label: 'Requests' },
            { value: 'vis', label: 'Visitors', disabled: !visitorsKnown },
            { value: 'bw', label: 'Bandwidth' },
          ]}
        />
      }
    >
      <div
        role="img"
        aria-label={`${legend.map((l) => l.label).join(', ')} per bucket over the ${rangeText}`}
        className="flex h-[170px] items-end gap-1 border-b border-gray-200 dark:border-gray-700"
      >
        {series.map((bucket, i) => (
          <span
            key={bucket.start}
            title={`${bucketLabel(bucket.start, bucketSeconds)} · ${format(totals[i])}${bucket.live ? ' · live' : ''}`}
            className="flex h-full flex-1 flex-col justify-end gap-px rounded-[3px]"
            style={{
              opacity: bucket.live ? 0.75 : 1,
              backgroundImage: bucket.live ? hatch : 'none',
            }}
          >
            {segments(bucket)
              .slice()
              .reverse()
              .map((s) => (
                <span
                  key={s.label}
                  style={{ height: `${Math.round((s.value / max) * 160)}px`, background: s.color }}
                />
              ))}
          </span>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-xs text-gray-500 dark:text-gray-400">
        {labels.map((label, i) => (
          <span key={`${label}-${i}`}>
            {label}
            {i === labels.length - 1 && hasLive ? ' · live' : ''}
          </span>
        ))}
      </div>
      <div className="mt-2 flex flex-wrap justify-between gap-2.5">
        <div className="flex flex-wrap gap-3.5 text-xs text-gray-500 dark:text-gray-400">
          {legend.map((l) => (
            <span key={l.label} className="inline-flex items-center gap-1.5">
              <i
                className="inline-block h-2.5 w-2.5 rounded-[3px]"
                style={{ background: l.color }}
              />
              {l.label}
            </span>
          ))}
          {hasLive && (
            <span className="inline-flex items-center gap-1.5">
              <i
                className="inline-block h-2.5 w-2.5 rounded-[3px] bg-[var(--v2-track)]"
                style={{ backgroundImage: hatch }}
              />
              live
            </span>
          )}
        </div>
        {totals[peakIndex] > 0 && (
          <span className="text-xs text-gray-500 dark:text-gray-400">
            Peak {bucketLabel(series[peakIndex].start, bucketSeconds)} · {format(totals[peakIndex])}
          </span>
        )}
      </div>
    </Card>
  );
};

interface StatusPanelProps {
  rows: WebAnalyticsRow[];
  total: number;
  status: StatusColors;
  onOpenStatus: (code: string) => void;
  onOpenAll: () => void;
}

/** Status codes as a donut of the share of requests. */
export const StatusPanel = ({
  rows,
  total,
  status,
  onOpenStatus,
  onOpenAll,
}: StatusPanelProps): JSX.Element => {
  const [all, setAll] = useState(false);
  const shown = all ? rows : rows.slice(0, 7);
  const sum = rows.reduce((s, r) => s + r.hits, 0) || 1;
  return (
    <PanelFrame
      title="Status codes"
      subtitle="Share of requests"
      more={{
        expanded: all,
        total: rows.length > 7 ? rows.length : 0,
        onToggle: () => setAll((v) => !v),
        expandLabel: 'All codes',
        collapseLabel: 'Top 7',
      }}
      menu={[
        { key: 'logs', label: 'Open in Logs', onClick: onOpenAll },
        {
          key: 'csv',
          label: 'Download CSV',
          onClick: () =>
            downloadText(
              'status-codes.csv',
              toCSV([
                ['Status', 'Requests', 'Share'],
                ...rows.map((r) => [r.key, r.hits, formatShare(r.hits / sum)]),
              ]),
              'text/csv',
            ),
        },
      ]}
    >
      {rows.length === 0 ? (
        <div className="py-4 text-center text-sm text-gray-500 dark:text-gray-400">
          No requests in this range
        </div>
      ) : (
        <span className="flex flex-wrap items-center gap-4">
          <Donut
            size={130}
            label="Status codes"
            center={formatCount(total)}
            centerSub="requests"
            parts={rows.map((r) => ({
              key: r.key,
              value: r.hits,
              color: statusColor(r.key, status),
            }))}
          />
          <span className="flex min-w-[150px] flex-1 flex-col gap-1.5">
            {shown.map((r) => (
              <button
                key={r.key}
                type="button"
                onClick={() => onOpenStatus(r.key)}
                title="Open these requests in Logs"
                className="flex cursor-pointer items-center gap-2 border-0 bg-transparent p-0 text-left text-[13px] text-gray-800 dark:text-gray-100"
              >
                <span
                  className="h-2.5 w-2.5 rounded-[3px]"
                  style={{ background: statusColor(r.key, status) }}
                />
                <span className="flex-1 font-mono">{r.key}</span>
                <strong className="font-semibold">{formatCount(r.hits)}</strong>
                <span className="w-12 text-right text-gray-500 dark:text-gray-400">
                  {formatShare(r.hits / sum)}
                </span>
              </button>
            ))}
          </span>
        </span>
      )}
    </PanelFrame>
  );
};

interface HeatmapPanelProps {
  hours: { hour: string; requests: number }[];
  accent: string;
  now: Date;
}

/** Requests by weekday and hour over the last 7 days, in the viewer's time zone. */
export const HeatmapPanel = ({ hours, accent, now }: HeatmapPanelProps): JSX.Element => {
  const map = useMemo(() => buildHeatmap(hours, now), [hours, now]);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const pad = (n: number) => String(n).padStart(2, '0');
  const download = () =>
    downloadText(
      'time-distribution.csv',
      toCSV([
        ['Day', ...Array.from({ length: 24 }, (_, h) => `${pad(h)}:00`)],
        ...map.rows.map((r) => [r.day, ...r.cells]),
      ]),
      'text/csv',
    );
  return (
    <PanelFrame
      title="Time distribution"
      subtitle={`Requests by weekday and hour, last 7 days (your time zone, ${zone})`}
      menu={[{ key: 'csv', label: 'Download CSV', onClick: download }]}
    >
      <div className="overflow-x-auto">
        <div
          className="grid min-w-[420px] gap-0.5 text-[10.5px]"
          style={{ gridTemplateColumns: '36px repeat(24, 1fr)' }}
        >
          <span />
          {Array.from({ length: 24 }, (_, h) => (
            <span key={h} className="text-center text-gray-500 dark:text-gray-400">
              {h % 3 === 0 ? pad(h) : ''}
            </span>
          ))}
          {map.rows.map((row) => [
            <span key={`${row.day}-label`} className="text-gray-500 dark:text-gray-400">
              {row.label}
            </span>,
            ...row.cells.map((value, h) => (
              <span
                key={`${row.day}-${h}`}
                title={`${row.label} ${pad(h)}:00 · ${formatCount(value)} requests`}
                className="h-4 rounded-[3px]"
                style={{
                  background: withAlpha(accent, map.max ? 0.08 + (value / map.max) * 0.8 : 0.06),
                }}
              />
            )),
          ])}
        </div>
      </div>
      <p className="m-0 mt-2 text-xs text-gray-500 dark:text-gray-400">
        {map.busiest
          ? `Busiest: ${map.busiest.label} ${pad(map.busiest.hour)}:00. Quietest: ${pad(map.quietestHour ?? 0)}:00.`
          : 'No requests in the last 7 days.'}
      </p>
    </PanelFrame>
  );
};
