import { InfoCircleOutlined, SearchOutlined } from '@ant-design/icons';
import { App, Button, Empty, Spin, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import { type Session, correlationApi } from '../../../entity/correlation';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { colorForService } from '../../traces/ui/traceColors';
import { SessionDetailComponent } from './SessionDetailComponent';
import { formatWindow, triggerTypeColor, triggerTypeLabel } from './sessionFormat';

interface Props {
  projectId: string;
  contentHeight: number;
}

// Stated plainly so the grouping isn't mistaken for AI root-cause analysis.
const SUBTITLE_TEXT = 'Grouped by service + time + known anomaly signals';
const SUBTITLE_TOOLTIP =
  'Not AI/semantic root-cause analysis. Sessions group signals that share a service and an ' +
  'overlapping time window, and only when a genuine anomaly signal is present: an ERROR log, ' +
  'a non-OK span status, or a shared trace ID link.';

export const SessionsListComponent = ({ projectId, contentHeight }: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [isSearching, setIsSearching] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null);

  const handleSearch = async () => {
    if (!timeRange) return;

    setIsSearching(true);
    setHasSearched(true);
    try {
      const response = await correlationApi.listSessions(
        projectId,
        timeRange.from.toISOString(),
        timeRange.to.toISOString(),
        50,
      );
      setSessions(response.sessions);
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
        <Tag color={colorForService(value)} variant="solid">
          {value}
        </Tag>
      ),
    },
    {
      title: 'Window',
      dataIndex: 'windowStart',
      render: (_, session) => formatWindow(session.windowStart, session.windowEnd),
    },
    {
      title: 'Trigger type',
      dataIndex: 'triggerType',
      render: (value: string) => (
        <Tag color={triggerTypeColor(value)}>{triggerTypeLabel(value)}</Tag>
      ),
    },
    {
      title: 'Trigger summary',
      dataIndex: 'triggerSummary',
      render: (value: string | undefined) => value || <span className="text-gray-300">—</span>,
    },
    {
      title: 'Created at',
      dataIndex: 'createdAt',
      render: (value: string) => dayjs(value).format('YYYY-MM-DD HH:mm:ss'),
      sorter: (a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime(),
    },
  ];

  if (selectedSessionId) {
    return (
      <div
        className="grow overflow-auto rounded bg-white p-4 shadow dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <SessionDetailComponent
          projectId={projectId}
          sessionId={selectedSessionId}
          onBack={() => setSelectedSessionId(null)}
        />
      </div>
    );
  }

  return (
    <div
      className="grow overflow-auto rounded bg-white p-4 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-1 flex items-center gap-2">
        <span className="text-sm text-gray-500 dark:text-gray-400">{SUBTITLE_TEXT}</span>
        <Tooltip title={SUBTITLE_TOOLTIP}>
          <InfoCircleOutlined className="text-gray-400" />
        </Tooltip>
      </div>

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <TimeRangePickerComponent onChange={setTimeRange} />

        <Button
          type="primary"
          icon={<SearchOutlined />}
          onClick={handleSearch}
          loading={isSearching}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          Search
        </Button>
      </div>

      {!hasSearched && !timeRange && (
        <div className="flex justify-center py-8">
          <Spin />
        </div>
      )}

      {hasSearched && sessions.length === 0 && !isSearching && (
        <Empty description="No incident sessions in this time range" />
      )}

      {sessions.length > 0 && (
        <Table<Session>
          size="small"
          rowKey="id"
          dataSource={sessions}
          columns={columns}
          loading={isSearching}
          pagination={{ pageSize: 20 }}
          onRow={(session) => ({
            className: 'cursor-pointer',
            onClick: () => setSelectedSessionId(session.id),
          })}
        />
      )}
    </div>
  );
};
