import { CloseOutlined } from '@ant-design/icons';
import { Input, Select, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import { type AiUsageFilter, type AiUsageSessionEvent, aiUsageApi } from '../../../entity/aiUsage';
import { colorFor, describeEvent } from '../lib/describeEvent';

interface Props {
  projectId: string;
  filter: AiUsageFilter;
  refreshToken: number;
}

type EventRow = AiUsageSessionEvent & { key: string };

const PAGE_SIZE = 50;

const FieldsTable = ({ fields }: { fields: Record<string, string> }): React.JSX.Element => (
  <div className="grid grid-cols-[minmax(160px,max-content)_1fr] gap-x-4 gap-y-1 font-mono text-xs">
    {Object.keys(fields)
      .sort()
      .map((key) => (
        <React.Fragment key={key}>
          <div className="text-gray-500 dark:text-gray-400">{key}</div>
          <div className="break-all text-gray-800 dark:text-gray-200">{fields[key]}</div>
        </React.Fragment>
      ))}
  </div>
);

export const EventsTable = ({ projectId, filter, refreshToken }: Props): React.JSX.Element => {
  const [events, setEvents] = useState<EventRow[]>([]);
  const [eventTypes, setEventTypes] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [eventType, setEventType] = useState<string>();
  const [sessionId, setSessionId] = useState<string>();
  const [search, setSearch] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setPage(1);
  }, [projectId, filter, eventType, sessionId, search]);

  useEffect(() => {
    let cancelled = false;
    setIsLoading(true);
    const offset = (page - 1) * PAGE_SIZE;
    aiUsageApi
      .getEvents(projectId, {
        ...filter,
        event: eventType,
        sessionId,
        search,
        limit: PAGE_SIZE,
        offset,
      })
      .then((response) => {
        if (cancelled) return;
        setEvents(response.events.map((e, i) => ({ ...e, key: `${offset + i}` })));
        setEventTypes(response.eventTypes);
        setTotal(response.total);
        setError(null);
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setIsLoading(false));
    return () => {
      cancelled = true;
    };
  }, [projectId, filter, eventType, sessionId, search, page, refreshToken]);

  const columns: ColumnsType<EventRow> = [
    {
      title: 'Time',
      dataIndex: 'timestamp',
      width: 170,
      render: (value: number) => (
        <span className="font-mono text-xs">{dayjs(value).format('DD MMM HH:mm:ss.SSS')}</span>
      ),
    },
    {
      title: 'Event',
      dataIndex: 'event',
      width: 190,
      render: (event: string, row) => <Tag color={colorFor(event, row.fields)}>{event}</Tag>,
    },
    {
      title: 'Session',
      key: 'session',
      width: 110,
      render: (_, row) =>
        row.fields.session_id ? (
          <a
            className="font-mono text-xs"
            onClick={(e) => {
              e.stopPropagation();
              setSessionId(row.fields.session_id);
            }}
          >
            {row.fields.session_id.slice(0, 8)}
          </a>
        ) : null,
    },
    {
      title: 'User',
      key: 'user',
      ellipsis: true,
      width: 200,
      render: (_, row) => row.fields.user_email,
    },
    {
      title: 'Details',
      key: 'details',
      render: (_, row) => describeEvent(row.event, row.fields),
    },
  ];

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Select
          allowClear
          size="small"
          placeholder="All events"
          className="!w-56"
          value={eventType}
          onChange={setEventType}
          options={eventTypes.map((value) => ({ value, label: value }))}
        />
        {sessionId && (
          <Tag closeIcon={<CloseOutlined />} onClose={() => setSessionId(undefined)}>
            Session {sessionId.slice(0, 8)}
          </Tag>
        )}
        <Input.Search
          allowClear
          size="small"
          placeholder="Search fields"
          className="!w-64"
          onSearch={(value) => setSearch(value.trim())}
        />
        <span className="ml-auto text-xs text-gray-500 dark:text-gray-400">
          {total.toLocaleString()} events
        </span>
      </div>
      {error && <div className="mb-2 text-sm text-red-600 dark:text-red-400">{error}</div>}
      <Table<EventRow>
        size="small"
        columns={columns}
        dataSource={events}
        loading={isLoading}
        expandable={{
          expandedRowRender: (row) => <FieldsTable fields={row.fields} />,
          expandRowByClick: true,
        }}
        pagination={{
          current: page,
          pageSize: PAGE_SIZE,
          total,
          showSizeChanger: false,
          onChange: setPage,
        }}
        scroll={{ x: 'max-content' }}
      />
    </div>
  );
};
