import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Alert, Button, Segmented, Spin, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useState } from 'react';

import {
  type LlmBreakdownRow,
  type LlmCall,
  type LlmCallsResponse,
  type LlmGroupBy,
  type LlmOverview,
  type LlmTimeseriesPoint,
  aiUsageApi,
} from '../../../entity/aiUsage';
import { absoluteChange, relativeChange } from '../../../shared/lib/periodChange';
import { Card, KpiTile } from '../../../shared/ui/v2';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { formatCount, formatMs, formatPercent, formatTime, formatUsd } from '../lib/format';
import { costCell, errorRate, toUsagePoints } from '../lib/llmApps';
import { bucketLabel, bucketTotals } from '../lib/series';
import { UsageChart } from './UsageChart';
import { InfoTip } from './bits';

interface Props {
  projectId: string;
  onOpenDataSources: () => void;
  onOpenTrace: (traceId: string, at: string) => void;
  onOpenPrices: () => void;
}

const GROUPS: { value: LlmGroupBy; label: string }[] = [
  { value: 'service', label: 'Service' },
  { value: 'model', label: 'Model' },
  { value: 'provider', label: 'Provider' },
  { value: 'operation', label: 'Operation' },
];

const breakdownColumns = (keyTitle: string): ColumnsType<LlmBreakdownRow> => [
  { title: keyTitle, dataIndex: 'key', ellipsis: true, render: (key: string) => key || '(none)' },
  { title: 'Calls', dataIndex: 'calls', align: 'right', render: formatCount },
  {
    title: 'Input tokens',
    dataIndex: 'inputTokens',
    align: 'right',
    render: (value: number, row) => (row.hasTokens ? formatCount(value) : '-'),
  },
  {
    title: 'Output tokens',
    dataIndex: 'outputTokens',
    align: 'right',
    render: (value: number, row) => (row.hasTokens ? formatCount(value) : '-'),
  },
  {
    title: 'Errors',
    key: 'errors',
    align: 'right',
    render: (_, row) => {
      const rate = errorRate(row.errors, row.calls);
      return (
        <span className={rate >= 0.02 ? 'text-[var(--v2-red)]' : undefined}>
          {formatPercent(rate)}
        </span>
      );
    },
  },
  { title: 'p95', dataIndex: 'p95Ms', align: 'right', render: formatMs },
  {
    title: 'Est. cost',
    key: 'cost',
    align: 'right',
    render: (_, row) => {
      const text = costCell(row);
      return row.costUsd === null ? (
        <span className="text-gray-500 dark:text-gray-400">{text}</span>
      ) : (
        text
      );
    },
  },
];

const describeCall = (call: LlmCall): string => {
  const parts = [call.operation, call.model].filter(Boolean);
  if (call.errorType) parts.push(`error.type=${call.errorType}`);
  if (call.finishReasons.length) parts.push(`finish=${call.finishReasons.join(',')}`);
  return parts.join(' · ');
};

/** Failed calls first, then the slowest, each call once. */
const mergeCalls = (calls: LlmCallsResponse | null): LlmCall[] => {
  if (!calls) return [];
  const seen = new Set<string>();
  return [...calls.failed, ...calls.slowest].filter((call) => {
    const key = `${call.traceId}/${call.spanId}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
};

export const LlmAppsComponent = ({
  projectId,
  onOpenDataSources,
  onOpenTrace,
  onOpenPrices,
}: Props): React.JSX.Element => {
  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [groupBy, setGroupBy] = useState<LlmGroupBy>('service');
  const [overview, setOverview] = useState<LlmOverview | null>(null);
  const [calls, setCalls] = useState<LlmCallsResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);

  const range = useMemo(
    () =>
      timeRange ? { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() } : null,
    [timeRange],
  );

  useEffect(() => {
    if (!range) return;
    let cancelled = false;
    setIsLoading(true);
    aiUsageApi
      .getLlmOverview(projectId, { ...range, groupBy, compare: 'previous' })
      .then((response) => {
        if (cancelled) return;
        setOverview(response);
        setError(null);
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setIsLoading(false));
    return () => {
      cancelled = true;
    };
  }, [projectId, range, groupBy, refreshToken]);

  useEffect(() => {
    if (!range) return;
    let cancelled = false;
    aiUsageApi
      .getLlmCalls(projectId, { ...range, limit: 10 })
      .then((response) => !cancelled && setCalls(response))
      .catch(() => !cancelled && setCalls(null));
    return () => {
      cancelled = true;
    };
  }, [projectId, range, refreshToken]);

  const summary = overview?.summary;
  const hasData = !!summary && summary.calls > 0;
  const listedCalls = mergeCalls(calls);
  const groupLabel = GROUPS.find((group) => group.value === groupBy)?.label ?? 'Service';
  const unpriced = summary?.unpricedModels.length ?? 0;
  // The window just before the range, for change pills; rates and latencies need calls in it.
  const previous = overview?.previous;
  const previousCalls = previous?.calls ? previous : undefined;
  const trendOf = (value: (point: LlmTimeseriesPoint) => number) =>
    overview
      ? bucketTotals(overview.timeseries, value, overview.bucketSeconds).map((point) => point.value)
      : [];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-auto min-w-[260px] flex-1 text-xs text-gray-500 dark:text-gray-400">
          Applications instrumented with the OpenTelemetry gen_ai conventions
          {overview?.latestCallAt ? (
            <span>
              {' '}
              · Latest call {formatTime(overview.latestCallAt)} (
              {dayjs(overview.latestCallAt).fromNow()})
            </span>
          ) : null}
        </div>
        <TimeRangePickerComponent compact defaultPreset="24h" onChange={setTimeRange} />
        <Tooltip title="Refresh">
          <Button
            size="small"
            icon={<ReloadOutlined />}
            aria-label="Refresh"
            onClick={() => setRefreshToken((token) => token + 1)}
          />
        </Tooltip>
      </div>

      {error && <Alert type="error" showIcon title={error} />}

      {!overview && isLoading && (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {overview && !hasData && (
        <Card
          className="items-center px-5 py-10 text-center"
          bodyClassName="flex flex-col items-center gap-3"
        >
          <strong className="text-base font-semibold text-gray-900 dark:text-gray-50">
            No LLM calls in this range
          </strong>
          <p className="m-0 max-w-[60ch] text-sm text-gray-600 dark:text-gray-300">
            Spans with <code>gen_ai.operation.name</code> show up here: OpenLLMetry, OpenLIT and the
            OpenTelemetry instrumentations for OpenAI, Anthropic, Bedrock, Vertex AI, LangChain and
            others. Point the app&apos;s OTLP exporter at this project.
          </p>
          <Button type="primary" onClick={onOpenDataSources}>
            Open Data Sources
          </Button>
        </Card>
      )}

      {overview && summary && hasData && range && (
        <div className={`flex flex-col gap-4 ${isLoading ? 'opacity-60' : ''}`}>
          <section
            aria-label="LLM totals"
            className="grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] gap-4"
          >
            <KpiTile
              label="Calls"
              value={formatCount(summary.calls)}
              note={`${summary.services} service${summary.services === 1 ? '' : 's'}`}
              change={relativeChange(summary.calls, previous?.calls, 'up')}
              trend={trendOf((point) => point.calls)}
            />
            <KpiTile
              label="Input tokens"
              value={formatCount(summary.inputTokens)}
              note={
                summary.cachedTokens ? `${formatCount(summary.cachedTokens)} cached` : undefined
              }
              change={relativeChange(summary.inputTokens, previous?.inputTokens, 'up')}
              trend={trendOf((point) => point.inputTokens)}
              color="var(--v2-blue)"
            />
            <KpiTile
              label="Output tokens"
              value={formatCount(summary.outputTokens)}
              change={relativeChange(summary.outputTokens, previous?.outputTokens, 'up')}
              trend={trendOf((point) => point.outputTokens)}
              color="var(--v2-purple)"
            />
            <KpiTile
              label="Errors"
              value={formatPercent(errorRate(summary.errors, summary.calls))}
              note={`${formatCount(summary.errors)} calls`}
              change={
                previousCalls &&
                absoluteChange(
                  errorRate(summary.errors, summary.calls) * 100,
                  errorRate(previousCalls.errors, previousCalls.calls) * 100,
                  'down',
                  (points) => `${points.toFixed(1)} pts`,
                )
              }
            />
            <KpiTile
              label="p95 latency"
              value={formatMs(summary.p95Ms)}
              note={`p50 ${formatMs(summary.p50Ms)}`}
              change={absoluteChange(summary.p95Ms, previousCalls?.p95Ms, 'down', formatMs)}
            />
            <KpiTile
              label="Est. cost"
              value={formatUsd(summary.costUsd)}
              change={relativeChange(summary.costUsd, previous?.costUsd, 'down')}
              icon={
                <InfoTip
                  label="Est. cost"
                  text="From the model prices in Settings. Obzly doesn't fetch prices."
                />
              }
              note={
                unpriced
                  ? `${unpriced} model${unpriced === 1 ? ' has' : 's have'} no price`
                  : undefined
              }
            />
          </section>
          {unpriced > 0 && (
            <div className="flex flex-wrap items-center gap-2 text-[13px] text-gray-600 dark:text-gray-300">
              <span
                aria-hidden="true"
                className="inline-block h-2 w-2 rounded-full bg-[var(--v2-orange)]"
              />
              No price for <span className="font-mono">{summary.unpricedModels.join(', ')}</span>.
              <Button type="link" size="small" className="!p-0" onClick={onOpenPrices}>
                Set model prices
              </Button>
            </div>
          )}

          <Card
            title="Tokens over time, by model"
            subtitle={`Input and output tokens per ${bucketLabel(overview.bucketSeconds)}`}
          >
            <UsageChart
              points={toUsagePoints(overview.timeseries)}
              metric="tokens"
              bucketSeconds={overview.bucketSeconds}
              from={Date.parse(range.from)}
              to={Date.parse(range.to)}
              height={240}
            />
          </Card>

          <Card
            title="Breakdown"
            extra={
              <Segmented
                size="small"
                value={groupBy}
                onChange={(value) => setGroupBy(value as LlmGroupBy)}
                options={GROUPS}
              />
            }
          >
            <Table<LlmBreakdownRow>
              size="small"
              rowKey="key"
              columns={breakdownColumns(groupLabel)}
              dataSource={overview.breakdown}
              pagination={{ pageSize: 10, hideOnSinglePage: true }}
              scroll={{ x: 'max-content' }}
            />
          </Card>

          <Card title="Slowest and failed calls">
            {listedCalls.length === 0 ? (
              <div className="text-sm text-gray-500 dark:text-gray-400">No calls to list</div>
            ) : (
              <ul className="m-0 flex list-none flex-col p-0">
                {listedCalls.map((call) => (
                  <li
                    key={`${call.traceId}/${call.spanId}`}
                    className="flex flex-wrap items-center gap-3 border-t border-gray-200 py-2 text-[13px] first:border-t-0 dark:border-gray-700"
                  >
                    <Tooltip title={call.isError ? 'Failed' : 'Slow'}>
                      <span
                        role="img"
                        aria-label={call.isError ? 'Failed' : 'Slow'}
                        className={`inline-block h-2 w-2 rounded-full ${
                          call.isError ? 'bg-[var(--v2-red)]' : 'bg-[var(--v2-orange)]'
                        }`}
                      />
                    </Tooltip>
                    <span className="w-36 font-mono text-xs text-gray-500 tabular-nums dark:text-gray-400">
                      {formatTime(call.timestamp)}
                    </span>
                    <span className="w-40 truncate font-medium text-gray-900 dark:text-gray-50">
                      {call.service}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs text-gray-500 dark:text-gray-400">
                      {describeCall(call)}
                    </span>
                    <span className="w-20 text-right font-mono text-gray-700 tabular-nums dark:text-gray-200">
                      {formatMs(call.durationMs)}
                    </span>
                    <Button
                      type="link"
                      size="small"
                      onClick={() => onOpenTrace(call.traceId, dayjs(call.timestamp).toISOString())}
                    >
                      Open trace
                    </Button>
                  </li>
                ))}
              </ul>
            )}
            <p className="m-0 mt-2 border-t border-gray-200 pt-3 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
              Prompts and completions are never shown here, even when an app sends them.
            </p>
          </Card>
        </div>
      )}
    </div>
  );
};
