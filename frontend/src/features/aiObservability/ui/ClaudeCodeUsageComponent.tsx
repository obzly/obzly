import { LoadingOutlined, ReloadOutlined } from '@ant-design/icons';
import { Button, Segmented, Select, Spin, Table, Tabs } from 'antd';
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
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { formatCount, formatMs, formatPercent, formatTime, formatUsd } from '../lib/format';
import { AiOnboardingPanel } from './AiOnboardingPanel';
import { EventsTable } from './EventsTable';
import { SessionDrawer } from './SessionDrawer';
import { SessionsTable } from './SessionsTable';
import { StatTile } from './StatTile';
import { UsageChart, type UsageMetric } from './UsageChart';

interface Props {
  projectId: string;
  onOpenDataSources: () => void;
}

const COST_TOOLTIP =
  "Claude Code's API-equivalent estimate. On Pro, Max and Team plans this is not what you're billed.";

const CARD = 'rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800';

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

const Section = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
    <h3 className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">{title}</h3>
    {children}
  </div>
);

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

  const changeTab = (key: string) => {
    setActiveTab(key as TabKey);
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
      .getOverview(projectId, { ...filter, groupBy })
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
  const hasAnyData =
    !!overview && overview.filterOptions.users.length + overview.filterOptions.models.length > 0;
  const toOptions = (values: string[] = []) => values.map((value) => ({ value, label: value }));

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="mr-auto">
          <div className="text-xs text-gray-500 dark:text-gray-400">
            Claude Code usage from its OpenTelemetry events
            {overview?.latestEventAt ? (
              <span>
                {' '}
                · Latest event {formatTime(overview.latestEventAt)} (
                {dayjs(overview.latestEventAt).fromNow()})
              </span>
            ) : null}
          </div>
        </div>
        <Select
          allowClear
          showSearch
          size="small"
          placeholder="All users"
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
          className="!w-52"
          value={model}
          onChange={setModel}
          options={toOptions(overview?.filterOptions.models)}
        />
        <Select
          allowClear
          size="small"
          placeholder="All terminals"
          className="!w-36"
          value={terminal}
          onChange={setTerminal}
          options={toOptions(overview?.filterOptions.terminals)}
        />
        <TimeRangePickerComponent compact defaultPreset="24h" onChange={setTimeRange} />
        <Button
          size="small"
          icon={<ReloadOutlined />}
          onClick={() => setRefreshToken((token) => token + 1)}
        />
      </div>

      {error && <div className="mb-4 text-sm text-red-600 dark:text-red-400">{error}</div>}

      {!overview && isLoading && (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}

      {(isSiteOutdated || (overview && !hasAnyData)) && (
        <AiOnboardingPanel isSiteOutdated={isSiteOutdated} onOpenDataSources={onOpenDataSources} />
      )}

      {overview && summary && hasAnyData && filter && (
        <Tabs
          activeKey={activeTab}
          onChange={changeTab}
          items={[
            {
              key: 'dashboard',
              label: 'Dashboard',
              children: (
                <div className={`space-y-4 ${isLoading ? 'opacity-60' : ''}`}>
                  <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
                    <StatTile
                      label="Est. cost"
                      value={formatUsd(summary.costUsd)}
                      tooltip={COST_TOOLTIP}
                    />
                    <StatTile
                      label="Tokens"
                      value={formatCount(totalTokens)}
                      hint={`${formatCount(summary.outputTokens)} output`}
                    />
                    <StatTile
                      label="Cache hit rate"
                      value={totalInput ? formatPercent(summary.cacheReadTokens / totalInput) : '-'}
                      hint="Input served from prompt cache"
                    />
                    <StatTile
                      label="Requests"
                      value={formatCount(summary.requests)}
                      hint={`avg ${formatMs(summary.avgDurationMs)} · TTFT p50 ${formatMs(summary.p50TtftMs)}`}
                    />
                    <StatTile
                      label="Sessions"
                      value={formatCount(summary.sessions)}
                      hint={`${formatCount(summary.prompts)} prompts · ${formatCount(summary.toolCalls)} tool calls`}
                    />
                    <StatTile
                      label="Users"
                      value={formatCount(summary.users)}
                      hint={summary.apiErrors ? `${summary.apiErrors} API errors` : 'No API errors'}
                    />
                  </div>

                  <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
                    <div className="mb-2 flex flex-wrap items-center gap-2">
                      <h3 className="mr-auto text-sm font-semibold text-gray-700 dark:text-gray-200">
                        Usage over time
                      </h3>
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
                    </div>
                    <UsageChart
                      points={overview.timeseries}
                      metric={metric}
                      bucketSeconds={overview.bucketSeconds}
                      from={Date.parse(filter.from)}
                      to={Date.parse(filter.to)}
                      height={280}
                    />
                  </div>

                  <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
                    <Section title="Users">
                      <Table<AiUsageBreakdownRow>
                        size="small"
                        rowKey="key"
                        columns={breakdownColumns('User')}
                        dataSource={overview.users}
                        pagination={{ pageSize: 10, hideOnSinglePage: true }}
                        scroll={{ x: 'max-content' }}
                      />
                    </Section>
                    <Section title="Models">
                      <Table<AiUsageBreakdownRow>
                        size="small"
                        rowKey="key"
                        columns={breakdownColumns('Model')}
                        dataSource={overview.models}
                        pagination={{ pageSize: 10, hideOnSinglePage: true }}
                        scroll={{ x: 'max-content' }}
                      />
                    </Section>
                    <Section title="Tools">
                      <Table<AiUsageToolRow>
                        size="small"
                        rowKey="tool"
                        columns={toolColumns}
                        dataSource={overview.tools}
                        pagination={{ pageSize: 10, hideOnSinglePage: true }}
                        scroll={{ x: 'max-content' }}
                      />
                    </Section>
                    <Section title="API errors">
                      <Table<AiUsageErrorRow>
                        size="small"
                        rowKey={(row) => `${row.statusCode}|${row.model}|${row.error}`}
                        columns={errorColumns}
                        dataSource={overview.errors}
                        pagination={{ pageSize: 10, hideOnSinglePage: true }}
                        scroll={{ x: 'max-content' }}
                      />
                    </Section>
                  </div>
                </div>
              ),
            },
            {
              key: 'sessions',
              label: 'Sessions',
              children: (
                <div className={CARD}>
                  <SessionsTable
                    projectId={projectId}
                    filter={filter}
                    refreshToken={refreshToken}
                    onOpenSession={setOpenSession}
                  />
                </div>
              ),
            },
            {
              key: 'events',
              label: 'Events',
              children: (
                <div className={CARD}>
                  <EventsTable projectId={projectId} filter={filter} refreshToken={refreshToken} />
                </div>
              ),
            },
          ]}
        />
      )}

      <SessionDrawer
        projectId={projectId}
        session={openSession}
        onClose={() => setOpenSession(null)}
      />
    </div>
  );
};
