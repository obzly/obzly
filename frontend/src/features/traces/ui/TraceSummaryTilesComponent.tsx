import { LoadingOutlined } from '@ant-design/icons';
import { Spin } from 'antd';
import React from 'react';

import { type ChangeTone, KpiTile } from '../../../shared/ui/v2';
import type { TraceSummaryStats } from '../lib/useTraceSummary';
import { formatDurationMs } from './traceColors';

interface Props {
  stats: TraceSummaryStats | null;
  isLoading: boolean;
  scopeLabel: string;
  windowLabel: string;
}

type Change = { text: string; tone: ChangeTone } | undefined;

const NA = 'n/a';

// Under 1% either way reads as "no change".
const percentChange = (
  current: number | null,
  previous: number | null,
  upIsBad: boolean,
): Change => {
  if (current === null || previous === null || previous === 0) return undefined;
  const pct = ((current - previous) / previous) * 100;
  if (Math.abs(pct) < 1) return { text: '0%', tone: 'neutral' as const };
  const text = `${pct > 0 ? '+' : '−'}${Math.abs(pct) < 10 ? Math.abs(pct).toFixed(1) : Math.round(Math.abs(pct))}%`;
  return { text, tone: pct > 0 === upIsBad ? ('bad' as const) : ('good' as const) };
};

const splitDuration = (ms: number | null): { value: string; unit?: string } => {
  if (ms === null) return { value: NA };
  const text = formatDurationMs(ms);
  const match = text.match(/^([\d.]+)(\D+)$/);
  return match ? { value: match[1], unit: match[2] } : { value: text };
};

export const TraceSummaryTilesComponent = ({
  stats,
  isLoading,
  scopeLabel,
  windowLabel,
}: Props): React.JSX.Element => {
  const current = stats?.current;
  const previous = stats?.previous;
  const minutes = stats?.windowMinutes ?? 0;

  const rate = (count: number | null | undefined) =>
    count === null || count === undefined || minutes <= 0 ? null : count / minutes;
  const currentRate = rate(current?.count);
  const errorRate = (totals: TraceSummaryStats['current'] | undefined) =>
    totals && totals.count && totals.errors !== null ? (totals.errors / totals.count) * 100 : null;
  const currentErrorRate = errorRate(current);
  const previousErrorRate = errorRate(previous);

  const requestsChange: Change = (() => {
    const change = percentChange(current?.count ?? null, previous?.count ?? null, false);
    // More or less traffic is neither good nor bad by itself.
    return change && { ...change, tone: 'neutral' };
  })();

  const errorRateChange: Change = (() => {
    if (currentErrorRate === null || previousErrorRate === null) return undefined;
    const diff = currentErrorRate - previousErrorRate;
    if (Math.abs(diff) < 0.05) return { text: '0 pt', tone: 'neutral' };
    return {
      text: `${diff > 0 ? '+' : '−'}${Math.abs(diff).toFixed(1)} pt`,
      tone: diff > 0 ? 'bad' : 'good',
    };
  })();

  const p50 = splitDuration(current?.p50Ms ?? null);
  const p95 = splitDuration(current?.p95Ms ?? null);
  const unitLabel = stats && !stats.rootSpansOnly ? 'Spans' : 'Requests';
  const vsNote = `vs previous ${windowLabel}`;

  return (
    <section aria-label="Summary" className="flex flex-col gap-2">
      <div className="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
        <span>{scopeLabel}</span>
        {isLoading && <Spin size="small" indicator={<LoadingOutlined spin />} />}
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <KpiTile
          label={unitLabel}
          value={
            currentRate === null
              ? NA
              : currentRate.toLocaleString(undefined, {
                  maximumFractionDigits: currentRate < 10 ? 1 : 0,
                })
          }
          unit={currentRate === null ? undefined : '/min'}
          change={requestsChange}
          note={
            current?.count !== null && current?.count !== undefined
              ? `${current.count.toLocaleString()} in range · ${vsNote}`
              : vsNote
          }
          trend={stats?.countTrend}
          color="var(--v2-accent)"
        />
        <KpiTile
          label="Error rate"
          value={currentErrorRate === null ? NA : currentErrorRate.toFixed(1)}
          unit={currentErrorRate === null ? undefined : '%'}
          change={errorRateChange}
          note={
            current?.errors !== null && current?.errors !== undefined && current.count
              ? `${current.errors.toLocaleString()} with errors · ${vsNote}`
              : vsNote
          }
          trend={stats?.errorRateTrend}
          color="var(--v2-red)"
        />
        <KpiTile
          label="p50"
          value={p50.value}
          unit={p50.unit}
          change={percentChange(current?.p50Ms ?? null, previous?.p50Ms ?? null, true)}
          note={vsNote}
          trend={stats?.p50Trend}
          color="var(--v2-blue)"
        />
        <KpiTile
          label="p95"
          value={p95.value}
          unit={p95.unit}
          change={percentChange(current?.p95Ms ?? null, previous?.p95Ms ?? null, true)}
          note={vsNote}
          trend={stats?.p95Trend}
          color="var(--v2-orange)"
        />
      </div>
    </section>
  );
};
