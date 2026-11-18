import { InfoCircleOutlined, SearchOutlined, SettingOutlined } from '@ant-design/icons';
import { App, Button, Empty, Spin, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import { type Session, correlationApi } from '../../../entity/correlation';
import { Card, PageHeader } from '../../../shared/ui/v2';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { useServiceColor } from '../../traces/ui/traceColors';
import { IncidentNotificationsModal } from './IncidentNotificationsModal';
import { SessionDetailComponent } from './SessionDetailComponent';
import { SessionsKpiTiles } from './SessionsKpiTiles';
import { formatWindow, triggerTypeColor, triggerTypeLabel } from './sessionFormat';

interface Props {
  projectId: string;
  contentHeight: number;
  /** Opens this session's detail (a notification's "Open in Obzly" link). */
  initialSessionId?: string;
  /** Project owners and admins change incident notifications; others see them read-only. */
  canManageNotifications?: boolean;
}

// Stated plainly so the grouping isn't mistaken for AI root-cause analysis.
const SUBTITLE_TEXT = 'Grouped by service + time + known anomaly signals';
const SUBTITLE_TOOLTIP =
  'Not AI/semantic root-cause analysis. Sessions group signals that share a service and an ' +
  'overlapping time window, and only when a genuine anomaly signal is present: an ERROR log, ' +
  'a non-OK span status, or a shared trace ID link.';

const SESSION_LIMIT = 50;
const PAGE_SIZE = 20;

const CONTAINER_CLASS =
  'flex grow flex-col gap-4 overflow-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800';

export const SessionsListComponent = ({
  projectId,
  contentHeight,
  initialSessionId,
  canManageNotifications = false,
}: Props): React.JSX.Element => {
  const serviceColor = useServiceColor();
  const { message } = App.useApp();

  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [searchedRange, setSearchedRange] = useState<TimeRange | null>(null);
  const [isSearching, setIsSearching] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(
    initialSessionId ?? null,
  );
  const [isNotificationsOpen, setIsNotificationsOpen] = useState(false);

  useEffect(() => {
    if (initialSessionId) setSelectedSessionId(initialSessionId);
  }, [initialSessionId]);

  const handleSearch = async () => {
    if (!timeRange) return;

    setIsSearching(true);
    setHasSearched(true);
    try {
      const response = await correlationApi.listSessions(
        projectId,
        timeRange.from.toISOString(),
        timeRange.to.toISOString(),
        SESSION_LIMIT,
      );
      setSessions(response.sessions);
      setSearchedRange(timeRange);
    } catch (e) {
      message.error((e as Error).message);
    } finally {
      setIsSearching(false);
    }
  };

  useEffect(() => {
    if (timeRange && !hasSearched) {
      handleSearch();
    }
  }, [timeRange]);

  const columns: ColumnsType<Session> = [
    {
      title: 'Service',
      dataIndex: 'serviceName',
      render: (value: string) => (
        <Tag color={serviceColor(value)} variant="solid" className="rounded-full">
          {value}
        </Tag>
      ),
    },
    {
      title: 'Window',
      dataIndex: 'windowStart',
      render: (_, session) => (
        <span className="font-mono text-xs whitespace-nowrap text-gray-700 dark:text-gray-200">
          {formatWindow(session.windowStart, session.windowEnd)}
        </span>
      ),
    },
    {
      title: 'Trigger type',
      dataIndex: 'triggerType',
      render: (value: string) => (
        <Tag color={triggerTypeColor(value)} className="rounded-full">
          {triggerTypeLabel(value)}
        </Tag>
      ),
    },
    {
      title: 'Trigger summary',
      dataIndex: 'triggerSummary',
      render: (value: string | undefined) =>
        value ? (
          <span className="text-gray-800 dark:text-gray-100">{value}</span>
        ) : (
          <span className="text-gray-300 dark:text-gray-600">—</span>
        ),
    },
    {
      title: 'Created at',
      dataIndex: 'createdAt',
      render: (value: string) => (
        <span className="font-mono text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
          {dayjs(value).format('YYYY-MM-DD HH:mm:ss')}
        </span>
      ),
      sorter: (a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime(),
    },
  ];

  if (selectedSessionId) {
    return (
      <div className={CONTAINER_CLASS} style={{ height: contentHeight }}>
        <SessionDetailComponent
          projectId={projectId}
          sessionId={selectedSessionId}
          onBack={() => setSelectedSessionId(null)}
        />
      </div>
    );
  }

  return (
    <div className={CONTAINER_CLASS} style={{ height: contentHeight }}>
      <PageHeader
        title="Incidents"
        subtitle={
          <span className="inline-flex items-center gap-1.5">
            {SUBTITLE_TEXT}
            {/* Click opens it too, so the explanation isn't hover-only. */}
            <Tooltip title={SUBTITLE_TOOLTIP} trigger={['hover', 'click']}>
              <button
                type="button"
                aria-label="How sessions are grouped"
                className="inline-flex h-5 w-5 cursor-pointer items-center justify-center rounded-full text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
              >
                <InfoCircleOutlined />
              </button>
            </Tooltip>
          </span>
        }
        actions={
          <>
            <Tooltip title="Incident notifications">
              <Button
                icon={<SettingOutlined />}
                aria-label="Incident notifications"
                onClick={() => setIsNotificationsOpen(true)}
              />
            </Tooltip>
            <TimeRangePickerComponent compact size="middle" onChange={setTimeRange} />
            <Button
              type="primary"
              icon={<SearchOutlined />}
              onClick={handleSearch}
              loading={isSearching}
              className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
            >
              Search
            </Button>
          </>
        }
      />

      {hasSearched && searchedRange && (
        <SessionsKpiTiles sessions={sessions} range={searchedRange} limit={SESSION_LIMIT} />
      )}

      {isNotificationsOpen && (
        <IncidentNotificationsModal
          projectId={projectId}
          canManage={canManageNotifications}
          onClose={() => setIsNotificationsOpen(false)}
        />
      )}

      <Card
        title="Sessions"
        subtitle={`Up to ${SESSION_LIMIT} sessions · click a row for its signals`}
      >
        {((!hasSearched && !timeRange) || (isSearching && sessions.length === 0)) && (
          <div className="flex justify-center py-8">
            <Spin />
          </div>
        )}

        {hasSearched && sessions.length === 0 && !isSearching && (
          <Empty description="No incident sessions in this time range" className="py-6" />
        )}

        {sessions.length > 0 && (
          <Table<Session>
            size="small"
            rowKey="id"
            dataSource={sessions}
            columns={columns}
            loading={isSearching}
            scroll={{ x: 'max-content' }}
            pagination={{
              pageSize: PAGE_SIZE,
              showTotal: (total, [from, to]) => `${from}–${to} of ${total} · ${PAGE_SIZE} per page`,
            }}
            onRow={(session) => ({
              className: 'cursor-pointer',
              onClick: () => setSelectedSessionId(session.id),
            })}
          />
        )}
      </Card>
    </div>
  );
};
