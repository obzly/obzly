import { SearchOutlined } from '@ant-design/icons';
import { Drawer, Input, Segmented, Select, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { type JSX, useMemo, useState } from 'react';

import type {
  IngestionSignal,
  IngestionSource,
  IngestionStatus,
} from '../../../../entity/projects';
import { Pill } from '../ProjectPageShell';
import { DataSourceIcon } from './DataSourceIcon';
import {
  type HealthFilter,
  byAttention,
  filterSources,
  healthCounts,
  totalRate,
} from './ingestionSources';
import {
  HEALTH_LABEL,
  formatAgo,
  formatRate,
  locationText,
  signalChip,
} from './ingestionStatusFormat';
import { HEALTH_TONE } from './ingestionTone';

interface Props {
  status: IngestionStatus;
  now: number;
  /** The health filter it opens with. */
  initialHealth: HealthFilter;
  typeName: (source: IngestionSource) => string | undefined;
  onClose: () => void;
}

const SIGNAL_OPTIONS: { value: IngestionSignal | 'all'; label: string }[] = [
  { value: 'all', label: 'All signals' },
  { value: 'logs', label: 'Logs' },
  { value: 'traces', label: 'Traces' },
  { value: 'metrics', label: 'Metrics' },
];

const time = (iso: string) => new Date(iso).getTime();

/** Every source sending to the project, as a sortable, filterable table. */
export const IngestionSourcesDrawer = ({
  status,
  now,
  initialHealth,
  typeName,
  onClose,
}: Props): JSX.Element => {
  const [health, setHealth] = useState<HealthFilter>(initialHealth);
  const [signal, setSignal] = useState<IngestionSignal | 'all'>('all');
  const [search, setSearch] = useState('');

  const counts = useMemo(() => healthCounts(status.sources), [status.sources]);
  const rows = useMemo(
    () => filterSources(status.sources, health, signal, search, typeName).sort(byAttention),
    [status.sources, health, signal, search, typeName],
  );

  const columns: ColumnsType<IngestionSource> = [
    {
      title: 'Source',
      key: 'name',
      sorter: (a, b) => a.name.localeCompare(b.name),
      render: (_, source) => {
        const detail = [typeName(source), locationText(source)].filter(Boolean).join(' · ');
        return (
          <span className="flex min-w-0 items-center gap-2.5">
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 bg-gray-50 dark:border-gray-700 dark:bg-gray-900/40">
              <DataSourceIcon sourceId={source.dataSource || 'custom-otlp'} size={16} />
            </span>
            <span className="min-w-0">
              <strong
                className="block truncate font-semibold text-gray-900 dark:text-gray-50"
                title={source.name}
              >
                {source.name}
              </strong>
              <span
                className="block truncate text-xs text-gray-500 dark:text-gray-400"
                title={detail}
              >
                {detail || 'Type and location unknown'}
              </span>
            </span>
          </span>
        );
      },
    },
    {
      title: 'Health',
      key: 'health',
      width: 110,
      sorter: byAttention,
      render: (_, source) => (
        <Pill tone={HEALTH_TONE[source.health]}>{HEALTH_LABEL[source.health]}</Pill>
      ),
    },
    {
      title: 'Signals',
      key: 'signals',
      render: (_, source) => (
        <span className="flex flex-wrap gap-1.5">
          {source.signals.map((s) => (
            <Tooltip
              key={s.signal}
              title={`${s.recentCount.toLocaleString()} in the last ${status.rateWindowMinutes} min · last ${formatAgo(s.lastSeen, now)}`}
            >
              <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs whitespace-nowrap text-gray-700 dark:bg-gray-700 dark:text-gray-200">
                {signalChip(s)}
              </span>
            </Tooltip>
          ))}
        </span>
      ),
    },
    {
      title: 'Rate',
      key: 'rate',
      width: 110,
      align: 'right',
      sorter: (a, b) => totalRate(a) - totalRate(b),
      render: (_, source) => (
        <span className="tabular-nums">{formatRate(Math.round(totalRate(source) * 10) / 10)}</span>
      ),
    },
    {
      title: 'Last data',
      key: 'lastSeen',
      width: 120,
      sorter: (a, b) => time(a.lastSeen) - time(b.lastSeen),
      render: (_, source) => (
        <Tooltip title={new Date(source.lastSeen).toLocaleString()}>
          <span className="whitespace-nowrap">{formatAgo(source.lastSeen, now)}</span>
        </Tooltip>
      ),
    },
    {
      title: 'First seen',
      key: 'firstSeen',
      width: 120,
      sorter: (a, b) => time(a.firstSeen) - time(b.firstSeen),
      render: (_, source) => (
        <Tooltip title={new Date(source.firstSeen).toLocaleString()}>
          <span className="whitespace-nowrap">{formatAgo(source.firstSeen, now)}</span>
        </Tooltip>
      ),
    },
  ];

  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(1100, Math.round(window.innerWidth * 0.94))}
      title={`Ingestion sources · ${counts.sending} of ${status.sources.length} sending`}
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Segmented<HealthFilter>
            value={health}
            onChange={setHealth}
            options={[
              { value: 'all', label: `All ${status.sources.length}` },
              { value: 'sending', label: `Sending ${counts.sending}` },
              { value: 'quiet', label: `Quiet ${counts.quiet}` },
              { value: 'stopped', label: `Stopped ${counts.stopped}` },
            ]}
          />
          <Select
            aria-label="Signal"
            value={signal}
            onChange={setSignal}
            options={SIGNAL_OPTIONS}
            className="w-36"
          />
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder="Search name, type, cluster, host…"
            aria-label="Search sources"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="min-w-0 flex-1 sm:max-w-xs"
          />
        </div>
        <div className="overflow-x-auto">
          <Table<IngestionSource>
            size="small"
            rowKey="id"
            columns={columns}
            dataSource={rows}
            pagination={rows.length > 50 ? { pageSize: 50, showSizeChanger: false } : false}
            locale={{ emptyText: 'No sources match these filters' }}
          />
        </div>
        <div className="text-xs text-gray-500 dark:text-gray-400">
          Rate is records per minute over the last {status.rateWindowMinutes} minutes. Quiet:
          nothing for {status.quietAfterMinutes} minutes; stopped: nothing for{' '}
          {status.stoppedAfterHours} hours.
          {status.truncated && ' Showing the most active sources only.'}
        </div>
      </div>
    </Drawer>
  );
};
