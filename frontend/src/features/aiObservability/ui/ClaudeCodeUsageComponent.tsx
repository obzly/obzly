import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Button, Segmented, Select, Spin, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useMemo, useState } from 'react';

import {
  type AiUsageBreakdownRow,
  type AiUsageErrorRow,
  type AiUsageFilter,
  type AiUsageOverview,
  type AiUsageSession,
  type AiUsageToolRow,
  aiUsageApi,
} from '../../../entity/aiUsage';
import { getHttpStatus } from '../../../shared/api';
import { absoluteChange, relativeChange } from '../../../shared/lib/periodChange';
import { Card, KpiTile } from '../../../shared/ui/v2';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { formatCount, formatMs, formatPercent, formatTime, formatUsd } from '../lib/format';
import { bucketLabel, bucketTotals, peakBucket } from '../lib/series';
import { AiOnboardingPanel } from './AiOnboardingPanel';
import { EventsTable } from './EventsTable';
import { SessionDrawer } from './SessionDrawer';
import { SessionsTable } from './SessionsTable';
import { UsageChart, type UsageMetric } from './UsageChart';
import { FootStats, InfoTip } from './bits';

interface Props {
  projectId: string;
  onOpenDataSources: () => void;
}

const COST_TOOLTIP =
  "Claude Code's API-equivalent estimate. On Pro, Max and Team plans this is not what you're billed.";

const METRIC_NOUN: Record<UsageMetric, string> = {
  costUsd: 'est. cost',
  tokens: 'tokens',
  requests: 'requests',
};

type TabKey = 'dashboard' | 'sessions' | 'events';

const TAB_STORAGE_KEY = 'ai_observability_tab';

const readStoredTab = (): TabKey => {
  try {
    const stored = localStorage.getItem(TAB_STORAGE_KEY);
    return stored === 'sessions' || stored === 'events' ? stored : 'dashboard';
  } catch {
    return 'dashboard';
  }
};

const breakdownColumns = (keyTitle: string): ColumnsType<AiUsageBreakdownRow> => [
  { title: keyTitle, dataIndex: 'key', ellipsis: true },
  { title: 'Est. cost', dataIndex: 'costUsd', align: 'right', render: formatUsd },
  { title: 'Requests', dataIndex: 'requests', align: 'right', render: formatCount },
  {
    title: 'Tokens (in / out)',
    key: 'tokens',
    align: 'right',
    render: (_, row) => `${formatCount(row.inputTokens)} / ${formatCount(row.outputTokens)}`,
  },
  { title: 'Cache read', dataIndex: 'cacheReadTokens', align: 'right', render: formatCount },
  { title: 'Sessions', dataIndex: 'sessions', align: 'right' },
  { title: 'Last seen', dataIndex: 'lastSeen', render: formatTime },
];

const toolColumns: ColumnsType<AiUsageToolRow> = [
  { title: 'Tool', dataIndex: 'tool' },
  { title: 'Calls', dataIndex: 'calls', align: 'right', render: formatCount },
  {
    title: 'Failed',
    dataIndex: 'failures',
    align: 'right',
    render: (value: number, row) =>
      row.calls ? `${value} (${formatPercent(value / row.calls)})` : value,
  },
  { title: 'Rejected', dataIndex: 'rejections', align: 'right' },
  { title: 'Avg', dataIndex: 'avgDurationMs', align: 'right', render: formatMs },
  { title: 'p95', dataIndex: 'p95DurationMs', align: 'right', render: formatMs },
];

const errorColumns: ColumnsType<AiUsageErrorRow> = [
  { title: 'Status', dataIndex: 'statusCode', width: 80 },
  { title: 'Error', dataIndex: 'error', ellipsis: true },
  { title: 'Model', dataIndex: 'model', ellipsis: true },
  { title: 'Count', dataIndex: 'count', align: 'right' },
  { title: 'Last seen', dataIndex: 'lastSeen', render: formatTime },
];

const TABS: { value: TabKey; label: string }[] = [
  { value: 'dashboard', label: 'Dashboard' },
  { value: 'sessions', label: 'Sessions' },
  { value: 'events', label: 'Events' },
];

const formatMetric = (metric: UsageMetric, value: number): string =>
  metric === 'costUsd' ? formatUsd(value) : formatCount(value);

export const ClaudeCodeUsageComponent = ({
  projectId,
  onOpenDataSources,
}: Props): React.JSX.Element => {
  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [user, setUser] = useState<string>();
  const [model, setModel] = useState<string>();
  const [terminal, setTerminal] = useState<string>();
  const [groupBy, setGroupBy] = useState<'model' | 'user'>('model');
  const [metric, setMetric] = useState<UsageMetric>('costUsd');
  const [overview, setOverview] = useState<AiUsageOverview | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A 404 means the Site has no AI usage API at all (older build), not a failed query.
  const [isSiteOutdated, setIsSiteOutdated] = useState(false);
  const [refreshToken, setRefreshToken] = useState(0);
  const [openSession, setOpenSession] = useState<AiUsageSession | null>(null);
  const [activeTab, setActiveTab] = useState<TabKey>(readStoredTab);

  // Sessions and Events stay mounted once opened so their search and page survive a switch.
  const [visited, setVisited] = useState<Set<TabKey>>(() => new Set([activeTab]));

  const changeTab = (key: TabKey) => {
    setActiveTab(key);
    setVisited((prev) => new Set(prev).add(key));
    try {
      localStorage.setItem(TAB_STORAGE_KEY, key);
    } catch {
      // Storage unavailable (private mode); the tab just isn't remembered.
    }
  };

  // Memoized so child effects only rerun on real changes.
  const filter = useMemo<AiUsageFilter | null>(
    () =>
      timeRange
        ? {
            from: timeRange.from.toISOString(),
            to: timeRange.to.toISOString(),
            user,
            model,
            terminal,
          }
        : null,
    [timeRange, user, model, terminal],
  );

  useEffect(() => {
    if (!filter) return;
    let cancelled = false;
    setIsLoading(true);
    aiUsageApi
      .getOverview(projectId, { ...filter, groupBy, compare: 'previous' })
      .then((response) => {
        if (cancelled) return;
        setOverview(response);
        setError(null);
        setIsSiteOutdated(false);
      })
      .catch((e: Error) => {
        if (cancelled) return;
        if (getHttpStatus(e) === 404) {
          setOverview(null);
          setError(null);
          setIsSiteOutdated(true);
        } else {
          setError(e.message);
        }
      })
      .finally(() => !cancelled && setIsLoading(false));
    return () => {
      cancelled = true;
    };
  }, [projectId, filter, groupBy, refreshToken]);

  const summary = overview?.summary;
  const totalInput = summary
    ? summary.inputTokens + summary.cacheReadTokens + summary.cacheCreationTokens
    : 0;
  const totalTokens = summary ? totalInput + summary.outputTokens : 0;
  // The window just before the range, for change pills.
  const previous = overview?.previous;
  const previousInput = previous
    ? previous.inputTokens + previous.cacheReadTokens + previous.cacheCreationTokens
    : 0;
  const hasAnyData =
    !!overview && overview.filterOptions.users.length + overview.filterOptions.models.length > 0;
  const toOptions = (values: string[] = []) => values.map((value) => ({ value, label: value }));

  // Sparklines and the chart footer come from the same timeseries as the chart.
  const trendOf = (key: UsageMetric) =>
    overview
      ? bucketTotals(overview.timeseries, (point) => point[key], overview.bucketSeconds)
      : [];
  const metricSeries = trendOf(metric);
  const peak = peakBucket(metricSeries);
  const metricTotal = metricSeries.reduce((sum, point) => sum + point.value, 0);
  const cacheHitRate = totalInput && summary ? summary.cacheReadTokens / totalInput : null;
  const thirdFoot =
    metric === 'costUsd'
      ? { label: 'Cache hits', value: cacheHitRate === null ? 'n/a' : formatPercent(cacheHitRate) }
      : metric === 'tokens'
        ? {
            label: 'Output share',
            value:
              totalTokens && summary ? formatPercent(summary.outputTokens / totalTokens) : 'n/a',
          }
        : { label: 'API errors', value: summary ? formatCount(summary.apiErrors) : 'n/a' };

  return (
    <div className="flex flex-col gap-4">
      <Card bodyClassName="flex flex-col gap-3" className="!gap-0 !py-3.5">
        <div className="flex flex-wrap items-center gap-2">
          <div className="mr-auto min-w-[240px] flex-1 text-xs text-gray-500 dark:text-gray-400">
            Claude Code usage from its OpenTelemetry events
            {overview?.latestEventAt ? (
              <span>
                {' '}
                · Latest event {formatTime(overview.latestEventAt)} (
                {dayjs(overview.latestEventAt).fromNow()})
              </span>
            ) : null}
          </div>
          <Select
            allowClear
            showSearch
            size="small"
            placeholder="All users"
            aria-label="All users"
            className="!w-52"
            value={user}
            onChange={setUser}
            options={toOptions(overview?.filterOptions.users)}
          />
          <Select
            allowClear
            showSearch
            size="small"
            placeholder="All models"
            aria-label="All models"
            className="!w-52"
            value={model}
            onChange={setModel}
            options={toOptions(overview?.filterOptions.models)}
          />
          <Select
            allowClear
            size="small"
            placeholder="All terminals"
            aria-label="All terminals"
            className="!w-36"
            value={terminal}
            onChange={setTerminal}
            options={toOptions(overview?.filterOptions.terminals)}
          />
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
        {overview && summary && hasAnyData && filter && (
          <Segmented<TabKey>
            className="self-start"
            value={activeTab}
            onChange={changeTab}
            options={TABS}
          />
        )}
      </Card>

      {error && <div className="text-sm text-[var(--v2-red)]">{error}</div>}

      {!overview && isLoading && (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {(isSiteOutdated || (overview && !hasAnyData)) && (
        <AiOnboardingPanel isSiteOutdated={isSiteOutdated} onOpenDataSources={onOpenDataSources} />
      )}

      {overview && summary && hasAnyData && filter && activeTab === 'dashboard' && (
        <div className={`flex flex-col gap-4 ${isLoading ? 'opacity-60' : ''}`}>
          <section
            aria-label="Claude Code totals"
            className="grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] gap-4"
          >
            <KpiTile
              label="Est. cost"
              value={formatUsd(summary.costUsd)}
              icon={<InfoTip label="Est. cost" text={COST_TOOLTIP} />}
              change={relativeChange(summary.costUsd, previous?.costUsd, 'down')}
              trend={trendOf('costUsd').map((point) => point.value)}
            />
            <KpiTile
              label="Tokens"
              value={formatCount(totalTokens)}
              note={`${formatCount(summary.outputTokens)} output`}
              change={relativeChange(
                totalTokens,
                previous && previousInput + previous.outputTokens,
                'up',
              )}
              trend={trendOf('tokens').map((point) => point.value)}
              color="var(--v2-blue)"
            />
            <KpiTile
              label="Cache hit rate"
              value={cacheHitRate === null ? 'n/a' : formatPercent(cacheHitRate)}
              note="Input served from prompt cache"
              change={
                cacheHitRate !== null && previous && previousInput
                  ? absoluteChange(
                      cacheHitRate * 100,
                      (previous.cacheReadTokens / previousInput) * 100,
                      'up',
                      (points) => `${points.toFixed(1)} pts`,
                    )
                  : undefined
              }
            />
            <KpiTile
              label="Requests"
              value={formatCount(summary.requests)}
              note={`avg ${formatMs(summary.avgDurationMs)} · TTFT p50 ${formatMs(summary.p50TtftMs)}`}
              change={relativeChange(summary.requests, previous?.requests, 'up')}
              trend={trendOf('requests').map((point) => point.value)}
              color="var(--v2-purple)"
            />
            <KpiTile
              label="Sessions"
              value={formatCount(summary.sessions)}
              change={relativeChange(summary.sessions, previous?.sessions, 'up')}
              note={`${formatCount(summary.prompts)} prompts · ${formatCount(summary.toolCalls)} tool calls`}
            />
            <KpiTile
              label="Users"
              value={formatCount(summary.users)}
              change={relativeChange(summary.users, previous?.users, 'up')}
              note={summary.apiErrors ? `${summary.apiErrors} API errors` : 'No API errors'}
            />
          </section>

          <Card
            title="Usage over time"
            subtitle={`Stacked ${METRIC_NOUN[metric]} per ${bucketLabel(overview.bucketSeconds)}`}
            extra={
              <>
                <Segmented
                  size="small"
                  value={metric}
                  onChange={(value) => setMetric(value as UsageMetric)}
                  options={[
                    { label: 'Est. cost', value: 'costUsd' },
                    { label: 'Tokens', value: 'tokens' },
                    { label: 'Requests', value: 'requests' },
                  ]}
                />
                <Segmented
                  size="small"
                  value={groupBy}
                  onChange={(value) => setGroupBy(value as 'model' | 'user')}
                  options={[
                    { label: 'By model', value: 'model' },
                    { label: 'By user', value: 'user' },
                  ]}
                />
              </>
            }
            bodyClassName="flex flex-col gap-3"
          >
            <UsageChart
              points={overview.timeseries}
              metric={metric}
              bucketSeconds={overview.bucketSeconds}
              from={Date.parse(filter.from)}
              to={Date.parse(filter.to)}
              height={280}
            />
            <FootStats
              items={[
                {
                  label: `Highest ${bucketLabel(overview.bucketSeconds)}`,
                  value: peak
                    ? `${formatTime(peak.bucket)} · ${formatMetric(metric, peak.value)}`
                    : 'n/a',
                },
                { label: 'In this range', value: formatMetric(metric, metricTotal) },
                thirdFoot,
              ]}
            />
          </Card>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <Card title="Users">
              <Table<AiUsageBreakdownRow>
                size="small"
                rowKey="key"
                columns={breakdownColumns('User')}
                dataSource={overview.users}
                pagination={{ pageSize: 10, hideOnSinglePage: true }}
                scroll={{ x: 'max-content' }}
              />
            </Card>
            <Card title="Models">
              <Table<AiUsageBreakdownRow>
                size="small"
                rowKey="key"
                columns={breakdownColumns('Model')}
                dataSource={overview.models}
                pagination={{ pageSize: 10, hideOnSinglePage: true }}
                scroll={{ x: 'max-content' }}
              />
            </Card>
            <Card title="Tools">
              <Table<AiUsageToolRow>
                size="small"
                rowKey="tool"
                columns={toolColumns}
                dataSource={overview.tools}
                pagination={{ pageSize: 10, hideOnSinglePage: true }}
                scroll={{ x: 'max-content' }}
              />
            </Card>
            <Card title="API errors">
              <Table<AiUsageErrorRow>
                size="small"
                rowKey={(row) => `${row.statusCode}|${row.model}|${row.error}`}
                columns={errorColumns}
                dataSource={overview.errors}
                pagination={{ pageSize: 10, hideOnSinglePage: true }}
                scroll={{ x: 'max-content' }}
              />
            </Card>
          </div>
        </div>
      )}

      {overview && summary && hasAnyData && filter && visited.has('sessions') && (
        <Card className={activeTab === 'sessions' ? '' : '!hidden'}>
          <SessionsTable
            projectId={projectId}
            filter={filter}
            refreshToken={refreshToken}
            onOpenSession={setOpenSession}
          />
        </Card>
      )}

      {overview && summary && hasAnyData && filter && visited.has('events') && (
        <Card className={activeTab === 'events' ? '' : '!hidden'}>
          <EventsTable projectId={projectId} filter={filter} refreshToken={refreshToken} />
        </Card>
      )}

      <SessionDrawer
        projectId={projectId}
        session={openSession}
        onClose={() => setOpenSession(null)}
      />
    </div>
  );
};
