import {
  ApartmentOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
} from '@ant-design/icons';
import { Input, Popconfirm, Segmented, Select, Table, Tooltip } from 'antd';
import type { ColumnsType, SorterResult } from 'antd/es/table/interface';
import { type ReactNode, memo, useMemo, useState } from 'react';

import type { CheckView } from '../../../entity/synthetics';
import { type FolderRow, type TreeRow, folderPaths, folderTree, inFolder } from '../lib/folders';
import { formatInterval, formatMs, formatPercent } from '../lib/format';
import { maintenanceTitle, withMaintenance } from '../lib/maintenance';
import {
  type MonitorStatus,
  STATUS_COLOR,
  STATUS_LABEL,
  STATUS_ORDER,
  byText,
  locationStatus,
} from '../lib/status';
import type { MonitorData } from '../lib/useMonitorData';
import { MaintenanceMarker } from './MaintenanceMarker';
import { IconAction, StatusBadge, StatusDot, TypeTag } from './parts';

type SortKey = 'status' | 'type' | 'name';
type SortOrder = 'ascend' | 'descend';
type Filter = 'all' | MonitorStatus;

interface Props {
  checks: CheckView[];
  data: Map<string, MonitorData>;
  locations: string[];
  canManage: boolean;
  busy: Set<string>;
  onOpen: (check: CheckView) => void;
  onEdit: (check: CheckView) => void;
  onClone: (check: CheckView) => void;
  onToggle: (check: CheckView) => void;
  onDelete: (check: CheckView) => void;
  /** Body height once the table is long enough to virtualise. */
  scrollHeight: number;
}

// Past this many visible rows only the rows on screen are rendered.
const VIRTUAL_FROM = 100;

const SORT_STORAGE_KEY = 'synthetics_sort';
const COLLAPSED_STORAGE_KEY = 'synthetics_collapsed_folders';
const ALL_FOLDERS = '\u0000all';

const readCollapsed = (): Set<string> => {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(COLLAPSED_STORAGE_KEY) ?? '[]');
    return new Set(Array.isArray(stored) ? stored.filter((key) => typeof key === 'string') : []);
  } catch {
    return new Set();
  }
};

const readSort = (): { key: SortKey; order: SortOrder } => {
  try {
    const stored = JSON.parse(localStorage.getItem(SORT_STORAGE_KEY) ?? '');
    if (
      ['status', 'type', 'name'].includes(stored.key) &&
      ['ascend', 'descend'].includes(stored.order)
    ) {
      return stored;
    }
  } catch {
    /* default below */
  }
  return { key: 'status', order: 'ascend' };
};

const FILTERS: { value: Filter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'down', label: STATUS_LABEL.down },
  { value: 'impacted', label: STATUS_LABEL.impacted },
  { value: 'warn', label: STATUS_LABEL.warn },
  { value: 'maintenance', label: STATUS_LABEL.maintenance },
  { value: 'up', label: STATUS_LABEL.up },
  { value: 'off', label: STATUS_LABEL.off },
];

const MonitorsTable = ({
  checks,
  data,
  locations,
  canManage,
  busy,
  onOpen,
  onEdit,
  onClone,
  onToggle,
  onDelete,
  scrollHeight,
}: Props) => {
  const [sort, setSort] = useState(readSort);
  const [filter, setFilter] = useState<Filter>('all');
  const [search, setSearch] = useState('');
  const [folder, setFolder] = useState(ALL_FOLDERS);
  const [collapsed, setCollapsed] = useState(readCollapsed);

  const statusOf = (check: CheckView): MonitorStatus => data.get(check.id)?.state.status ?? 'none';
  const folders = useMemo(() => folderPaths(checks), [checks]);
  const narrowed = filter !== 'all' || search.trim() !== '';

  const rows = useMemo(() => {
    const byName = (a: CheckView, b: CheckView) => byText(a.name, b.name);
    const sorters: Record<SortKey, (a: CheckView, b: CheckView) => number> = {
      status: (a, b) => STATUS_ORDER[statusOf(a)] - STATUS_ORDER[statusOf(b)] || byName(a, b),
      type: (a, b) => byText(a.type, b.type) || byName(a, b),
      name: byName,
    };
    const compare = sorters[sort.key];
    const shown = checks.filter(
      (check) =>
        (folder === ALL_FOLDERS || inFolder(check, folder)) &&
        (filter === 'all' || statusOf(check) === filter) &&
        check.name.toLowerCase().includes(search.trim().toLowerCase()),
    );
    return folderTree(shown, sort.order === 'ascend' ? compare : (a, b) => compare(b, a), statusOf);
  }, [checks, data, filter, search, folder, sort]);

  const { expandedKeys, visibleRows } = useMemo(() => {
    const keys: string[] = [];
    let visible = 0;
    const walk = (items: TreeRow[], shown: boolean) =>
      items.forEach((row) => {
        if (shown) visible += 1;
        if (row.kind !== 'folder') return;
        // A search or status filter opens every folder, so matches show.
        const open = narrowed || !collapsed.has(row.path);
        if (open) keys.push(row.key);
        walk(row.children, shown && open);
      });
    walk(rows, true);
    return { expandedKeys: keys, visibleRows: visible };
  }, [rows, collapsed, narrowed]);
  const virtual = visibleRows > VIRTUAL_FROM;

  const toggleFolder = (row: FolderRow, open: boolean) => {
    const next = new Set(collapsed);
    if (open) next.delete(row.path);
    else next.add(row.path);
    setCollapsed(next);
    try {
      localStorage.setItem(COLLAPSED_STORAGE_KEY, JSON.stringify([...next]));
    } catch {
      /* not remembered */
    }
  };

  // Sorting is done above (folders stay first), so antd only shows the carets.
  const sortable = (key: SortKey) => ({
    sorter: true,
    sortOrder: sort.key === key ? sort.order : null,
    sortDirections: ['ascend', 'descend', 'ascend'] as SortOrder[],
  });
  const forCheck = (render: (check: CheckView) => ReactNode) => (_: unknown, row: TreeRow) =>
    row.kind === 'check' ? render(row.check) : null;

  const columns: ColumnsType<TreeRow> = [
    {
      title: 'Status',
      key: 'status',
      width: 130,
      ...sortable('status'),
      render: forCheck((check) => {
        const parents = data.get(check.id)?.impactedBy;
        const badge = <StatusBadge status={statusOf(check)} />;
        if (statusOf(check) === 'maintenance') {
          return (
            <Tooltip title={maintenanceTitle(data.get(check.id)?.maintenance)}>{badge}</Tooltip>
          );
        }
        return parents ? (
          <Tooltip
            title={`${parents.map((parent) => parent.name).join(', ')} down: alerts muted until it recovers`}
          >
            {badge}
          </Tooltip>
        ) : (
          badge
        );
      }),
    },
    {
      title: 'Type',
      key: 'type',
      width: 90,
      ...sortable('type'),
      render: forCheck((check) => <TypeTag type={check.type} />),
    },
    {
      title: 'Name',
      key: 'name',
      width: 300,
      ...sortable('name'),
      render: (_, row) =>
        row.kind === 'folder' ? (
          <span className="inline-flex items-center gap-2 font-semibold whitespace-nowrap text-gray-800 dark:text-gray-100">
            {expandedKeys.includes(row.key) ? <FolderOpenOutlined /> : <FolderOutlined />}
            {row.name}
            <span className="text-xs font-normal text-gray-500 dark:text-gray-400">
              {row.total}
            </span>
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5">
            <span className="font-medium">{row.check.name}</span>
            <MaintenanceMarker maintenance={data.get(row.check.id)?.maintenance} />
            {(row.check.parentIds ?? []).length > 0 && (
              <Tooltip
                title={`Depends on ${row.check.parentIds
                  .map((id) => checks.find((item) => item.id === id)?.name ?? 'a deleted monitor')
                  .join(', ')}`}
              >
                <ApartmentOutlined className="text-xs text-gray-400" />
              </Tooltip>
            )}
          </span>
        ),
    },
    {
      title: 'Locations',
      key: 'locations',
      width: 160,
      onCell: (row) => (row.kind === 'folder' ? { colSpan: canManage ? 6 : 5 } : {}),
      render: (_, row) => {
        if (row.kind === 'folder') return <FolderCounts row={row} />;
        const check = row.check;
        const states = new Map(
          (data.get(check.id)?.state.locations ?? []).map((state) => [state.location, state]),
        );
        const shown = check.locations.length > 0 ? check.locations : locations;
        const inMaintenance = !!data.get(check.id)?.maintenance?.active;
        return (
          <span className="inline-flex flex-wrap gap-1">
            {shown.map((location) => {
              const state = states.get(location);
              const status: MonitorStatus = !check.enabled
                ? 'off'
                : state
                  ? withMaintenance(locationStatus(state), inMaintenance)
                  : 'none';
              return (
                <Tooltip key={location} title={`${location}: ${STATUS_LABEL[status]}`}>
                  <span
                    className="inline-block h-3 w-3 rounded-[3px]"
                    style={{ background: STATUS_COLOR[status] }}
                  />
                </Tooltip>
              );
            })}
          </span>
        );
      },
    },
    {
      title: 'Uptime',
      key: 'uptime',
      width: 90,
      align: 'right',
      onCell: hiddenForFolder,
      render: forCheck((check) => formatPercent(data.get(check.id)?.uptime)),
    },
    {
      title: 'p95',
      key: 'p95',
      width: 90,
      align: 'right',
      onCell: hiddenForFolder,
      render: forCheck((check) => formatMs(data.get(check.id)?.p95Ms)),
    },
    {
      title: 'Cert',
      key: 'cert',
      width: 80,
      align: 'right',
      onCell: hiddenForFolder,
      render: forCheck((check) => {
        const days = data.get(check.id)?.certDays;
        return days === undefined ? '—' : `${Math.floor(days)} d`;
      }),
    },
    {
      title: 'Interval',
      key: 'interval',
      width: 90,
      onCell: hiddenForFolder,
      render: forCheck((check) => (
        <span className="text-gray-500 dark:text-gray-400">
          {formatInterval(check.intervalSeconds)}
        </span>
      )),
    },
  ];
  if (canManage) {
    columns.push({
      title: 'Actions',
      key: 'actions',
      width: 140,
      onCell: hiddenForFolder,
      render: forCheck((check) => (
        <span className="inline-flex gap-0.5" onClick={(event) => event.stopPropagation()}>
          <IconAction title="Edit" icon={<EditOutlined />} onClick={() => onEdit(check)} />
          <IconAction
            title="Clone"
            icon={<CopyOutlined />}
            onClick={() => onClone(check)}
            loading={busy.has(`clone:${check.id}`)}
          />
          <IconAction
            title={check.enabled ? 'Disable' : 'Enable'}
            icon={check.enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />}
            onClick={() => onToggle(check)}
            loading={busy.has(`toggle:${check.id}`)}
          />
          <Popconfirm
            title={`Delete "${check.name}"?`}
            okText="Delete"
            okButtonProps={{ danger: true }}
            onConfirm={() => onDelete(check)}
          >
            <span>
              <IconAction title="Delete" icon={<DeleteOutlined />} danger />
            </span>
          </Popconfirm>
        </span>
      )),
    });
  }

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <Input.Search
          allowClear
          placeholder="Search monitors"
          className="max-w-xs"
          onChange={(event) => setSearch(event.target.value)}
        />
        {folders.length > 0 && (
          <Select
            value={folder}
            onChange={setFolder}
            className="min-w-44"
            aria-label="Folder"
            options={[
              { value: ALL_FOLDERS, label: 'All folders' },
              ...folders.map((path) => ({ value: path, label: path })),
            ]}
          />
        )}
        <Segmented<Filter>
          value={filter}
          onChange={setFilter}
          options={FILTERS.map((option) => ({
            value: option.value,
            label: (
              <span className="inline-flex items-center gap-1.5">
                {option.value === 'maintenance' && <StatusDot status="maintenance" size={8} />}
                {option.value !== 'all' && option.value !== 'maintenance' && (
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{
                      background:
                        option.value === 'impacted'
                          ? `linear-gradient(90deg, ${STATUS_COLOR.impacted} 50%, transparent 50%)`
                          : STATUS_COLOR[option.value],
                      boxShadow:
                        option.value === 'impacted'
                          ? `inset 0 0 0 1px ${STATUS_COLOR.impacted}`
                          : undefined,
                    }}
                  />
                )}
                {option.label}
              </span>
            ),
          }))}
        />
      </div>
      <Table<TreeRow>
        rowKey="key"
        size="middle"
        dataSource={rows}
        columns={columns}
        pagination={false}
        virtual={virtual}
        scroll={virtual ? { x: 1000, y: scrollHeight } : { x: 1000 }}
        showSorterTooltip={false}
        expandable={{
          expandIconColumnIndex: 2,
          expandedRowKeys: expandedKeys,
          indentSize: 18,
          onExpand: (open, row) => row.kind === 'folder' && toggleFolder(row, open),
        }}
        onRow={(row) => ({
          onClick: () =>
            row.kind === 'check'
              ? onOpen(row.check)
              : toggleFolder(row, !expandedKeys.includes(row.key)),
          className:
            row.kind === 'folder'
              ? 'cursor-pointer bg-gray-50 dark:bg-gray-900/40'
              : 'cursor-pointer',
        })}
        onChange={(_, __, sorter) => {
          const current = (Array.isArray(sorter) ? sorter[0] : sorter) as SorterResult<TreeRow>;
          const next = {
            key: (current.columnKey as SortKey) ?? 'status',
            order: (current.order ?? 'ascend') as SortOrder,
          };
          setSort(next);
          try {
            localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify(next));
          } catch {
            /* the sort just isn't remembered */
          }
        }}
        locale={{ emptyText: checks.length === 0 ? 'No monitors yet' : 'No monitors match' }}
      />
    </div>
  );
};

const hiddenForFolder = (row: TreeRow) => (row.kind === 'folder' ? { colSpan: 0 } : {});

/** A folder's monitors by status, problems first. */
const FolderCounts = ({ row }: { row: FolderRow }) => (
  <span className="inline-flex flex-wrap items-center gap-3 text-xs text-gray-600 dark:text-gray-300">
    {(Object.keys(STATUS_ORDER) as MonitorStatus[])
      .sort((a, b) => STATUS_ORDER[a] - STATUS_ORDER[b])
      .filter((status) => row.counts[status])
      .map((status) => (
        <span key={status} className="inline-flex items-center gap-1">
          <StatusDot status={status} size={8} />
          {row.counts[status]} {STATUS_LABEL[status].toLowerCase()}
        </span>
      ))}
  </span>
);

export const MonitorsTableComponent = memo(MonitorsTable);
