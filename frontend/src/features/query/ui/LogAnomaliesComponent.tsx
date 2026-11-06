import { FilterOutlined } from '@ant-design/icons';
import { Alert, Button, Drawer, Empty, Segmented, Spin, Tag, Tooltip } from 'antd';
import type dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import {
  type PatternChange,
  type PatternChangesResponse,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { levelColor } from '../lib/logLevels';
import type { TimeRange } from './TimeRangePickerComponent';

interface Props {
  projectId: string;
  open: boolean;
  /** The last run's search and range. */
  query: QueryNode | null;
  timeRange: TimeRange | null;
  onClose: () => void;
  /** Filters the search to one pattern (pattern_hash). */
  onShowPattern: (hash: string) => void;
}

type Baseline = 'previous' | 'day' | 'week';

const BASELINES: { value: Baseline; label: string }[] = [
  { value: 'previous', label: 'Previous period' },
  { value: 'day', label: 'Same time yesterday' },
  { value: 'week', label: 'Same time last week' },
];

const baselineRange = (
  range: TimeRange,
  baseline: Baseline,
): { from: dayjs.Dayjs; to: dayjs.Dayjs } => {
  if (baseline === 'day')
    return { from: range.from.subtract(1, 'day'), to: range.to.subtract(1, 'day') };
  if (baseline === 'week')
    return { from: range.from.subtract(1, 'week'), to: range.to.subtract(1, 'week') };
  const length = range.to.diff(range.from);
  return { from: range.from.subtract(length, 'ms'), to: range.from };
};

const SECTIONS: { kind: PatternChange['change']; title: string; empty: string }[] = [
  { kind: 'new', title: 'New patterns', empty: 'No pattern appeared that the baseline lacked' },
  { kind: 'spike', title: 'Spiking', empty: 'No pattern at least doubled' },
  { kind: 'drop', title: 'Dropped', empty: 'No pattern at least halved' },
];

const changeLabel = (change: PatternChange): string => {
  if (change.change === 'new') return 'new';
  if (change.change === 'spike') return `×${change.ratio.toFixed(change.ratio < 10 ? 1 : 0)}`;
  return `−${Math.round((1 - change.ratio) * 100)}%`;
};

/**
 * Message patterns of the last search that are new, spiking (at least 2x) or
 * dropping (at most half) against a baseline scaled to the same length.
 */
export const LogAnomaliesComponent = ({
  projectId,
  open,
  query,
  timeRange,
  onClose,
  onShowPattern,
}: Props): React.JSX.Element => {
  const [baseline, setBaseline] = useState<Baseline>('previous');
  const [data, setData] = useState<PatternChangesResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || !timeRange) return;
    const abort = new AbortController();
    const base = baselineRange(timeRange, baseline);
    setIsLoading(true);
    setError(null);
    setData(null);
    queryApi
      .getPatternChanges(
        projectId,
        {
          query: query ?? undefined,
          current: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
          baseline: { from: base.from.toISOString(), to: base.to.toISOString() },
        },
        abort.signal,
      )
      .then(setData)
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError((e as Error).message);
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });
    return () => abort.abort();
  }, [open, projectId, query, timeRange, baseline]);

  const renderChange = (change: PatternChange) => {
    const level = (change.level || 'unknown').toLowerCase();
    return (
      <li
        key={change.hash}
        className="flex items-start gap-2 rounded px-1 py-1 text-xs hover:bg-gray-100 dark:hover:bg-gray-700"
      >
        <span className="w-12 shrink-0 font-semibold" style={{ color: levelColor(level) }}>
          {level}
        </span>
        <Tooltip title={change.sample ? `e.g. ${change.sample}` : undefined} placement="left">
          <span className="min-w-0 flex-1 font-mono text-[11px] break-all text-gray-800 dark:text-gray-100">
            {change.pattern || change.hash}
          </span>
        </Tooltip>
        <span className="shrink-0 text-right text-gray-500 tabular-nums dark:text-gray-400">
          {change.current.toLocaleString()} vs {change.baseline.toLocaleString()}
        </span>
        <Tag
          className="!mr-0 shrink-0"
          color={change.change === 'drop' ? 'blue' : change.change === 'new' ? 'red' : 'orange'}
        >
          {changeLabel(change)}
        </Tag>
        <Tooltip title="Show these logs">
          <Button
            size="small"
            type="text"
            icon={<FilterOutlined />}
            aria-label="Show these logs"
            onClick={() => onShowPattern(change.hash)}
          />
        </Tooltip>
      </li>
    );
  };

  return (
    <Drawer title="Log anomalies" open={open} onClose={onClose} size={720}>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Segmented
            size="small"
            value={baseline}
            onChange={(value) => setBaseline(value as Baseline)}
            options={BASELINES}
          />
          {data && (
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {data.currentTotal.toLocaleString()} logs vs {data.baselineTotal.toLocaleString()} in
              the baseline
            </span>
          )}
        </div>
        <div className="text-xs text-gray-500 dark:text-gray-400">
          Message patterns of the last search, against the baseline scaled to the same length.
        </div>
        {error && <Alert type="error" showIcon message={error} />}
        {isLoading && (
          <div className="flex justify-center py-6">
            <Spin size="small" />
          </div>
        )}
        {data &&
          SECTIONS.map((section) => {
            const changes = data.changes.filter((c) => c.change === section.kind);
            return (
              <div key={section.kind}>
                <div className="mb-1 text-sm font-semibold text-gray-800 dark:text-gray-100">
                  {section.title}{' '}
                  <span className="font-normal text-gray-400">({changes.length})</span>
                </div>
                {changes.length === 0 ? (
                  <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={section.empty} />
                ) : (
                  <ul>{changes.map(renderChange)}</ul>
                )}
              </div>
            );
          })}
      </div>
    </Drawer>
  );
};
