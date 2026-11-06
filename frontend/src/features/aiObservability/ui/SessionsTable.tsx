import { Input, Table, Tag } from 'antd';
import type { ColumnsType, TablePaginationConfig } from 'antd/es/table';
import type { SorterResult } from 'antd/es/table/interface';
import React, { useEffect, useState } from 'react';

import {
  type AiUsageFilter,
  type AiUsageSession,
  type AiUsageSessionsParams,
  aiUsageApi,
} from '../../../entity/aiUsage';
import { formatCount, formatTime, formatUsd } from '../lib/format';

interface Props {
  projectId: string;
  filter: AiUsageFilter;
  refreshToken: number;
  onOpenSession: (session: AiUsageSession) => void;
}

type SortBy = NonNullable<AiUsageSessionsParams['sortBy']>;

const PAGE_SIZE = 20;

const SORT_KEYS: Record<string, SortBy> = {
  lastSeen: 'lastSeen',
  costUsd: 'cost',
  requests: 'requests',
  tokens: 'tokens',
};

export const SessionsTable = ({
  projectId,
  filter,
  refreshToken,
  onOpenSession,
}: Props): React.JSX.Element => {
  const [sessions, setSessions] = useState<AiUsageSession[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [sortBy, setSortBy] = useState<SortBy>('lastSeen');
  const [search, setSearch] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleTableChange = (
    pagination: TablePaginationConfig,
    _filters: unknown,
    sorter: SorterResult<AiUsageSession> | SorterResult<AiUsageSession>[],
  ) => {
    const single = Array.isArray(sorter) ? sorter[0] : sorter;
    const nextSort = single?.order
      ? (SORT_KEYS[String(single.columnKey)] ?? 'lastSeen')
      : 'lastSeen';
    if (nextSort !== sortBy) {
      setSortBy(nextSort);
      setPage(1);
    } else {
      setPage(pagination.current ?? 1);
    }
  };

  useEffect(() => {
    setPage(1);
  }, [projectId, filter, search]);

  useEffect(() => {
    let cancelled = false;
    setIsLoading(true);
    aiUsageApi
      .getSessions(projectId, {
        ...filter,
        search,
        sortBy,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      .then((response) => {
        if (cancelled) return;
        setSessions(response.sessions);
        setTotal(response.total);
        setError(null);
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setIsLoading(false));
    return () => {
      cancelled = true;
    };
  }, [projectId, filter, search, sortBy, page, refreshToken]);

  const sortOrderFor = (key: SortBy) => (sortBy === key ? ('descend' as const) : null);

  const columns: ColumnsType<AiUsageSession> = [
    {
      title: 'Session',
      dataIndex: 'sessionId',
      render: (id: string) => <span className="font-mono text-xs">{id.slice(0, 8)}</span>,
    },
    { title: 'User', dataIndex: 'user', ellipsis: true },
    {
      title: 'Models',
      dataIndex: 'models',
      render: (models: string[]) =>
        models.map((model) => (
          <Tag key={model} className="!mb-1">
            {model}
          </Tag>
        )),
    },
    {
      title: 'Last activity',
      key: 'lastSeen',
      dataIndex: 'lastSeen',
      sorter: true,
      sortDirections: ['descend'],
      sortOrder: sortOrderFor('lastSeen'),
      render: (value: number) => formatTime(value),
    },
    { title: 'Prompts', dataIndex: 'prompts', align: 'right' },
    {
      title: 'Requests',
      key: 'requests',
      dataIndex: 'requests',
      align: 'right',
      sorter: true,
      sortDirections: ['descend'],
      sortOrder: sortOrderFor('requests'),
    },
    { title: 'Tool calls', dataIndex: 'toolCalls', align: 'right' },
    {
      title: 'Tokens',
      key: 'tokens',
      dataIndex: 'tokens',
      align: 'right',
      sorter: true,
      sortDirections: ['descend'],
      sortOrder: sortOrderFor('tokens'),
      render: (value: number) => formatCount(value),
    },
    {
      title: 'Est. cost',
      key: 'costUsd',
      dataIndex: 'costUsd',
      align: 'right',
      sorter: true,
      sortDirections: ['descend'],
      sortOrder: sortOrderFor('cost'),
      render: (value: number) => formatUsd(value),
    },
    {
      title: 'Errors',
      dataIndex: 'errors',
      align: 'right',
      render: (value: number) =>
        value > 0 ? <span className="text-red-600 dark:text-red-400">{value}</span> : 0,
    },
  ];

  return (
    <div>
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-200">Sessions</h3>
        <Input.Search
          allowClear
          size="small"
          placeholder="Session ID or user"
          className="!w-64"
          onSearch={(value) => setSearch(value.trim())}
        />
      </div>
      {error && <div className="mb-2 text-sm text-red-600 dark:text-red-400">{error}</div>}
      <Table<AiUsageSession>
        size="small"
        rowKey="sessionId"
        columns={columns}
        dataSource={sessions}
        loading={isLoading}
        onChange={handleTableChange}
        pagination={{ current: page, pageSize: PAGE_SIZE, total, showSizeChanger: false }}
        onRow={(session) => ({
          onClick: () => onOpenSession(session),
          className: 'cursor-pointer',
        })}
        scroll={{ x: 'max-content' }}
      />
    </div>
  );
};
