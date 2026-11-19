import { FilterOutlined } from '@ant-design/icons';
import { Alert, Button, Drawer, Empty, Segmented, Spin, Tooltip } from 'antd';
import React, { useEffect, useState } from 'react';

import {
  type PatternChange,
  type PatternChangesResponse,
  type QueryNode,
  queryApi,
} from '../../../entity/query';
import { type Baseline, baselineRange } from '../lib/anomalies';
import { LevelPillComponent } from './LevelPillComponent';
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

const BASELINES: { value: Baseline; label: string }[] = [
  { value: 'previous', label: 'Previous period' },
  { value: 'day', label: 'Same time yesterday' },
  { value: 'week', label: 'Same time last week' },
];

const SECTIONS: { kind: PatternChange['change']; title: string; empty: string }[] = [
  { kind: 'new', title: 'New patterns', empty: 'No pattern appeared that the baseline lacked' },
  { kind: 'spike', title: 'Spiking', empty: 'No pattern at least doubled' },
  { kind: 'drop', title: 'Dropped', empty: 'No pattern at least halved' },
];

// New and spiking patterns are bad news, a drop is worth a look.
const CHANGE_PILL: Record<PatternChange['change'], string> = {
  new: 'bg-red-500/15 text-[var(--v2-red)]',
  spike: 'bg-orange-500/15 text-[var(--v2-orange)]',
  drop: 'bg-blue-500/15 text-[var(--v2-blue)]',
};

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
        className="flex items-start gap-2 rounded-lg bg-gray-50 px-2 py-1.5 text-xs hover:bg-gray-100 dark:bg-gray-900/50 dark:hover:bg-gray-700"
      >
        <span
          className={`inline-flex min-w-[44px] shrink-0 justify-center rounded-full px-2 py-0.5 font-medium ${CHANGE_PILL[change.change]}`}
        >
          {changeLabel(change)}
        </span>
        <LevelPillComponent level={level} className="mt-px shrink-0" />
        <Tooltip title={change.sample ? `e.g. ${change.sample}` : undefined} placement="left">
          <span className="min-w-0 flex-1 font-mono text-[11px] break-all text-gray-800 dark:text-gray-100">
            {change.pattern || change.hash}
          </span>
        </Tooltip>
        <span className="shrink-0 text-right text-gray-500 tabular-nums dark:text-gray-400">
          {change.current.toLocaleString()} vs {change.baseline.toLocaleString()}
        </span>
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
          <span className="text-xs text-gray-500 dark:text-gray-400">Compare with</span>
          <Segmented
            size="small"
            value={baseline}
            onChange={(value) => setBaseline(value as Baseline)}
            options={BASELINES}
          />
          {data && (
            <span className="ml-auto text-xs text-gray-500 dark:text-gray-400">
              {data.currentTotal.toLocaleString()} logs vs {data.baselineTotal.toLocaleString()} in
              the baseline
            </span>
          )}
        </div>
        <div className="text-xs text-gray-500 dark:text-gray-400">
          Message patterns of the last search, against the baseline scaled to the same length.
        </div>
        {error && <Alert type="error" showIcon message={error} />}
        {data?.patternVersionSince && (
          <Alert
            type="info"
            showIcon
            message="The baseline is from before patterns were regrouped (IDs, names and JSON values no longer make a pattern each), so new and dropped patterns leave out the ones that changed."
          />
        )}
        {isLoading && (
          <div className="flex justify-center py-6">
            <Spin size="small" />
          </div>
        )}
        {data &&
          SECTIONS.map((section) => {
            const changes = data.changes.filter((c) => c.change === section.kind);
            return (
              <div
                key={section.kind}
                className="rounded-2xl border border-gray-200 p-3 dark:border-gray-700"
              >
                <div className="mb-2 text-sm font-semibold text-gray-800 dark:text-gray-100">
                  {section.title}{' '}
                  <span className="font-normal text-gray-400">· {changes.length}</span>
                </div>
                {changes.length === 0 ? (
                  <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={section.empty} />
                ) : (
                  <ul className="flex flex-col gap-1">{changes.map(renderChange)}</ul>
                )}
              </div>
            );
          })}
      </div>
    </Drawer>
  );
};
