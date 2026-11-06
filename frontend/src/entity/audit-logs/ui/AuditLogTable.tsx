import { DownloadOutlined, SearchOutlined } from '@ant-design/icons';
import { App, Button, Input, Table } from 'antd';
import type { ColumnType, ColumnsType, TablePaginationConfig } from 'antd/es/table';
import type { FilterValue, SorterResult } from 'antd/es/table/interface';
import dayjs from 'dayjs';
import { useEffect, useRef, useState } from 'react';

import { downloadText, toCSV } from '../../../shared/lib';
import { getUserShortTimeFormat } from '../../../shared/time';
import type { AuditLog } from '../model/AuditLog';
import type { GetAuditLogsRequest } from '../model/GetAuditLogsRequest';
import type { GetAuditLogsResponse } from '../model/GetAuditLogsResponse';

const PAGE_SIZE_OPTIONS = [25, 50, 75, 100];
// The API pages at most 1000; an export stops at EXPORT_LIMIT rows.
const EXPORT_PAGE = 1000;
const EXPORT_LIMIT = 50_000;

interface Query {
  page: number;
  pageSize: number;
  user: string;
  action: string;
  sortBy: NonNullable<GetAuditLogsRequest['sortBy']>;
  sortOrder: NonNullable<GetAuditLogsRequest['sortOrder']>;
}

const initialQuery: Query = {
  page: 1,
  pageSize: PAGE_SIZE_OPTIONS[0],
  user: '',
  action: '',
  sortBy: 'createdAt',
  sortOrder: 'desc',
};

// Text search in a column header; filtering, sorting and paging run on the server.
function searchColumn(placeholder: string, value: string): Partial<ColumnType<AuditLog>> {
  return {
    filteredValue: value ? [value] : null,
    filterIcon: (filtered: boolean) => (
      <SearchOutlined className={filtered ? '!text-emerald-600' : undefined} />
    ),
    filterDropdown: ({ selectedKeys, setSelectedKeys, confirm, clearFilters }) => (
      <div className="flex w-64 flex-col gap-2 p-2" onKeyDown={(e) => e.stopPropagation()}>
        <Input
          autoFocus
          placeholder={placeholder}
          value={selectedKeys[0] as string | undefined}
          onChange={(e) => setSelectedKeys(e.target.value ? [e.target.value] : [])}
          onPressEnter={() => confirm()}
        />
        <div className="flex justify-end gap-2">
          <Button
            size="small"
            onClick={() => {
              clearFilters?.();
              confirm();
            }}
          >
            Reset
          </Button>
          <Button
            size="small"
            type="primary"
            icon={<SearchOutlined />}
            onClick={() => confirm()}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Search
          </Button>
        </div>
      </div>
    ),
  };
}

interface Props {
  title: string;
  /** Fetches one page; called again whenever the page, size, search or sort changes. */
  load: (request: GetAuditLogsRequest) => Promise<GetAuditLogsResponse>;
  /** Changing it (e.g. the project ID) reloads from the first page. */
  scopeKey?: string;
  showProjectColumn?: boolean;
  /** Shows Export CSV, saving the logs matching the search under this file name. */
  exportName?: string;
}

export function AuditLogTable({
  title,
  load,
  scopeKey,
  showProjectColumn = true,
  exportName,
}: Props) {
  const { message } = App.useApp();
  const [auditLogs, setAuditLogs] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [isLoading, setIsLoading] = useState(true);
  const [query, setQuery] = useState<Query>(initialQuery);
  const requestSeq = useRef(0);
  const [isExporting, setIsExporting] = useState(false);

  const exportCSV = async () => {
    setIsExporting(true);
    try {
      const rows: AuditLog[] = [];
      for (let offset = 0; offset < EXPORT_LIMIT; offset += EXPORT_PAGE) {
        const page = await load({
          limit: EXPORT_PAGE,
          offset,
          user: query.user,
          action: query.action,
          sortBy: query.sortBy,
          sortOrder: query.sortOrder,
        });
        rows.push(...page.auditLogs);
        if (page.auditLogs.length < EXPORT_PAGE) break;
      }
      downloadText(
        `${exportName}-${dayjs().format('YYYY-MM-DD')}.csv`,
        toCSV([
          ['Time (UTC)', 'User', 'Email', 'Action', 'Project'],
          ...rows.map((log) => [
            dayjs(log.createdAt).toISOString(),
            log.userName ?? (log.userEmail ? '' : 'System'),
            log.userEmail,
            log.message,
            log.projectName,
          ]),
        ]),
        'text/csv',
      );
      message.success(
        rows.length >= EXPORT_LIMIT
          ? `Exported the first ${EXPORT_LIMIT.toLocaleString()} logs; search to narrow it down`
          : `Exported ${rows.length.toLocaleString()} logs`,
      );
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to export audit logs');
    } finally {
      setIsExporting(false);
    }
  };

  useEffect(() => {
    setQuery(initialQuery);
  }, [scopeKey]);

  useEffect(() => {
    // Only the latest request's response is applied.
    const seq = ++requestSeq.current;
    setIsLoading(true);
    load({
      limit: query.pageSize,
      offset: (query.page - 1) * query.pageSize,
      user: query.user,
      action: query.action,
      sortBy: query.sortBy,
      sortOrder: query.sortOrder,
    })
      .then((response) => {
        if (seq !== requestSeq.current) return;
        setAuditLogs(response.auditLogs);
        setTotal(response.total);
      })
      .catch((error: unknown) => {
        if (seq !== requestSeq.current) return;
        message.error(error instanceof Error ? error.message : 'Failed to load audit logs');
      })
      .finally(() => {
        if (seq === requestSeq.current) setIsLoading(false);
      });
  }, [query, scopeKey]);

  const handleTableChange = (
    pagination: TablePaginationConfig,
    filters: Record<string, FilterValue | null>,
    sorter: SorterResult<AuditLog> | SorterResult<AuditLog>[],
  ) => {
    const sort = Array.isArray(sorter) ? sorter[0] : sorter;
    const user = (filters.user?.[0] as string | undefined) ?? '';
    const action = (filters.message?.[0] as string | undefined) ?? '';
    const pageSize = pagination.pageSize ?? query.pageSize;
    const filtersChanged =
      user !== query.user || action !== query.action || pageSize !== query.pageSize;

    setQuery({
      // A new search or page size starts again from the first page.
      page: filtersChanged ? 1 : (pagination.current ?? 1),
      pageSize,
      user,
      action,
      sortBy: sort?.order && sort.columnKey === 'user' ? 'user' : 'createdAt',
      // Clearing the sort returns to the default: newest first.
      sortOrder: sort?.order === 'ascend' ? 'asc' : 'desc',
    });
  };

  const sortOrderFor = (key: Query['sortBy']) =>
    query.sortBy === key ? (query.sortOrder === 'asc' ? 'ascend' : 'descend') : null;

  const allColumns: ColumnsType<AuditLog> = [
    {
      title: 'User',
      key: 'user',
      width: 300,
      sorter: true,
      sortOrder: sortOrderFor('user'),
      ...searchColumn('Search name or email', query.user),
      render: (_, record: AuditLog) => {
        if (!record.userEmail && !record.userName) {
          return (
            <span className="inline-block rounded-full bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600 dark:bg-gray-700 dark:text-gray-300">
              System
            </span>
          );
        }

        const displayText = record.userName
          ? `${record.userName} (${record.userEmail})`
          : record.userEmail;

        return (
          <span className="inline-block rounded-full bg-emerald-100 px-1.5 py-0.5 text-xs font-medium text-emerald-800">
            {displayText}
          </span>
        );
      },
    },
    {
      title: 'Action',
      dataIndex: 'message',
      key: 'message',
      ...searchColumn('Search actions', query.action),
      render: (text: string) => (
        <span className="text-xs text-gray-900 dark:text-gray-100">{text}</span>
      ),
    },
    {
      title: 'Project',
      dataIndex: 'projectName',
      key: 'projectName',
      width: 200,
      render: (projectName: string | undefined) => (
        <span
          className={`inline-block rounded-full px-1.5 py-0.5 text-xs font-medium ${
            projectName
              ? 'bg-blue-100 text-blue-800'
              : 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
          }`}
        >
          {projectName || '-'}
        </span>
      ),
    },
    {
      title: 'Created',
      dataIndex: 'createdAt',
      key: 'createdAt',
      width: 250,
      sorter: true,
      sortOrder: sortOrderFor('createdAt'),
      render: (createdAt: string) => {
        const date = dayjs(createdAt);
        const timeFormat = getUserShortTimeFormat();
        return (
          <span className="text-xs text-gray-700 dark:text-gray-200">
            {`${date.format(timeFormat.format)} (${date.fromNow()})`}
          </span>
        );
      },
    },
  ];

  const columns = showProjectColumn
    ? allColumns
    : allColumns.filter((column) => column.key !== 'projectName');

  const isFiltered = !!(query.user || query.action);

  return (
    <div className="max-w-[1200px]">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-bold text-gray-900 dark:text-gray-100">{title}</h2>
        <div className="flex items-center gap-3 text-sm text-gray-500 dark:text-gray-300">
          <span>
            {total} {isFiltered ? 'matching' : 'total'} {total === 1 ? 'log' : 'logs'}
          </span>
          {isFiltered && (
            <Button
              size="small"
              type="link"
              className="!px-0"
              onClick={() => setQuery({ ...query, page: 1, user: '', action: '' })}
            >
              Clear search
            </Button>
          )}
          {exportName && (
            <Button
              size="small"
              icon={<DownloadOutlined />}
              loading={isExporting}
              disabled={total === 0}
              onClick={exportCSV}
            >
              Export CSV
            </Button>
          )}
        </div>
      </div>

      <Table<AuditLog>
        columns={columns}
        dataSource={auditLogs}
        rowKey="id"
        size="small"
        className="mb-4"
        loading={isLoading}
        onChange={handleTableChange}
        pagination={{
          current: query.page,
          pageSize: query.pageSize,
          total,
          showSizeChanger: true,
          pageSizeOptions: PAGE_SIZE_OPTIONS,
          showTotal: (count, [from, to]) => `${from}-${to} of ${count}`,
        }}
      />
    </div>
  );
}
