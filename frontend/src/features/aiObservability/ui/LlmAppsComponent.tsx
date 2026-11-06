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
  aiUsageApi,
} from '../../../entity/aiUsage';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { formatCount, formatMs, formatPercent, formatTime, formatUsd } from '../lib/format';
import { costCell, errorRate, toUsagePoints } from '../lib/llmApps';
import { StatTile } from './StatTile';
import { UsageChart } from './UsageChart';

interface Props {
  projectId: string;
  onOpenDataSources: () => void;
  onOpenTrace: (traceId: string, at: string) => void;
  onOpenPrices: () => void;
}

const CARD = 'rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800';

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
        <span className={rate >= 0.02 ? 'text-red-600 dark:text-red-400' : undefined}>
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
      .getLlmOverview(projectId, { ...range, groupBy })
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

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="mr-auto text-xs text-gray-500 dark:text-gray-400">
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
        <Button
          size="small"
          icon={<ReloadOutlined />}
          aria-label="Refresh"
          onClick={() => setRefreshToken((token) => token + 1)}
        />
      </div>

      {error && <Alert type="error" showIcon className="mb-4" title={error} />}

      {!overview && isLoading && (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {overview && !hasData && (
        <div className={`${CARD} text-sm text-gray-700 dark:text-gray-200`}>
          <div className="mb-2 font-medium">No LLM calls in this range</div>
          <p className="mb-3 text-gray-600 dark:text-gray-300">
            Spans with <code>gen_ai.operation.name</code> show up here: OpenLLMetry, OpenLIT and the
            OpenTelemetry instrumentations for OpenAI, Anthropic, Bedrock, Vertex AI, LangChain and
            others. Point the app&apos;s OTLP exporter at this project.
          </p>
          <Button onClick={onOpenDataSources}>Open Data Sources</Button>
        </div>
      )}

      {overview && summary && hasData && range && (
        <div className={`space-y-4 ${isLoading ? 'opacity-60' : ''}`}>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
            <StatTile
              label="Calls"
              value={formatCount(summary.calls)}
              hint={`${summary.services} service${summary.services === 1 ? '' : 's'}`}
            />
            <StatTile
              label="Input tokens"
              value={formatCount(summary.inputTokens)}
              hint={
                summary.cachedTokens ? `${formatCount(summary.cachedTokens)} cached` : undefined
              }
            />
            <StatTile label="Output tokens" value={formatCount(summary.outputTokens)} />
            <StatTile
              label="Errors"
              value={formatPercent(errorRate(summary.errors, summary.calls))}
              hint={`${formatCount(summary.errors)} calls`}
            />
            <StatTile
              label="p95 latency"
              value={formatMs(summary.p95Ms)}
              hint={`p50 ${formatMs(summary.p50Ms)}`}
            />
            <StatTile
              label="Est. cost"
              value={formatUsd(summary.costUsd)}
              tooltip="From the model prices in Settings. Obzly doesn't fetch prices."
              hint={
                unpriced
                  ? `${unpriced} model${unpriced === 1 ? ' has' : 's have'} no price`
                  : undefined
              }
            />
          </div>
          {unpriced > 0 && (
            <div className="text-xs text-gray-500 dark:text-gray-400">
              No price for {summary.unpricedModels.join(', ')}.{' '}
              <Button type="link" size="small" className="!p-0" onClick={onOpenPrices}>
                Set model prices
              </Button>
            </div>
          )}

          <div className={CARD}>
            <h3 className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">
              Tokens over time, by model
            </h3>
            <UsageChart
              points={toUsagePoints(overview.timeseries)}
              metric="tokens"
              bucketSeconds={overview.bucketSeconds}
              from={Date.parse(range.from)}
              to={Date.parse(range.to)}
              height={240}
            />
          </div>

          <div className={CARD}>
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <h3 className="mr-auto text-sm font-semibold text-gray-700 dark:text-gray-200">
                Breakdown
              </h3>
              <Segmented
                size="small"
                value={groupBy}
                onChange={(value) => setGroupBy(value as LlmGroupBy)}
                options={GROUPS}
              />
            </div>
            <Table<LlmBreakdownRow>
              size="small"
              rowKey="key"
              columns={breakdownColumns(groupLabel)}
              dataSource={overview.breakdown}
              pagination={{ pageSize: 10, hideOnSinglePage: true }}
              scroll={{ x: 'max-content' }}
            />
          </div>

          <div className={CARD}>
            <h3 className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">
              Slowest and failed calls
            </h3>
            {listedCalls.length === 0 ? (
              <div className="text-sm text-gray-500 dark:text-gray-400">No calls to list</div>
            ) : (
              <ul className="divide-y divide-gray-100 dark:divide-gray-700">
                {listedCalls.map((call) => (
                  <li
                    key={`${call.traceId}/${call.spanId}`}
                    className="flex flex-wrap items-center gap-3 py-2 text-sm"
                  >
                    <Tooltip title={call.isError ? 'Failed' : 'Slow'}>
                      <span
                        aria-label={call.isError ? 'Failed' : 'Slow'}
                        className={`inline-block h-2 w-2 rounded-full ${
                          call.isError ? 'bg-red-600' : 'bg-amber-500'
                        }`}
                      />
                    </Tooltip>
                    <span className="w-36 text-gray-500 tabular-nums dark:text-gray-400">
                      {formatTime(call.timestamp)}
                    </span>
                    <span className="w-40 truncate font-medium text-gray-900 dark:text-gray-100">
                      {call.service}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-gray-700 dark:text-gray-300">
                      {describeCall(call)}
                    </span>
                    <span className="w-20 text-right text-gray-700 tabular-nums dark:text-gray-300">
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
            <p className="mt-2 mb-0 text-xs text-gray-500 dark:text-gray-400">
              Prompts and completions are never shown here, even when an app sends them.
            </p>
          </div>
        </div>
      )}
    </div>
  );
};
