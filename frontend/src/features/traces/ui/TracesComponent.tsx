import { SearchOutlined } from '@ant-design/icons';
import { App, Button, Checkbox, Drawer, Empty, InputNumber, Select, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import React, { useEffect, useState } from 'react';

import { type Span, type TraceSummary, traceApi } from '../../../entity/traces';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { TraceWaterfallComponent } from './TraceWaterfallComponent';
import { formatDurationMs } from './traceColors';

interface Props {
  projectId: string;
  contentHeight: number;
  /** Pre-selects a service, e.g. when opened from the service map. */
  initialServiceName?: string;
  /** Opens this trace, e.g. from a log ("Open trace"); at is the log's time. */
  initialTrace?: { traceId: string; at: string };
}

export const TracesComponent = ({
  projectId,
  contentHeight,
  initialServiceName,
  initialTrace,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [services, setServices] = useState<string[]>([]);
  const [operations, setOperations] = useState<string[]>([]);
  const [serviceName, setServiceName] = useState<string | undefined>(initialServiceName);
  const [operationName, setOperationName] = useState<string | undefined>(undefined);
  const [minDurationMs, setMinDurationMs] = useState<number | undefined>(undefined);
  const [maxDurationMs, setMaxDurationMs] = useState<number | undefined>(undefined);
  const [errorsOnly, setErrorsOnly] = useState(false);

  const [isSearching, setIsSearching] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);
  const [traces, setTraces] = useState<TraceSummary[]>([]);

  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(null);
  const [selectedSpans, setSelectedSpans] = useState<Span[] | null>(null);
  const [isLoadingTrace, setIsLoadingTrace] = useState(false);

  useEffect(() => {
    traceApi
      .listServices(projectId)
      .then((response) => setServices(response.services))
      .catch((e: unknown) => message.error((e as Error).message));
  }, [projectId]);

  useEffect(() => {
    if (!serviceName) {
      setOperations([]);
      setOperationName(undefined);
      return;
    }
    traceApi
      .listOperations(projectId, serviceName)
      .then((response) => setOperations(response.operations))
      .catch(() => setOperations([]));
  }, [projectId, serviceName]);

  const handleSearch = async () => {
    if (!timeRange) return;

    setIsSearching(true);
    setHasSearched(true);
    try {
      const response = await traceApi.listTraces(projectId, {
        timeRange: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
        serviceName,
        operationName,
        minDurationMs,
        maxDurationMs,
        errorsOnly,
        limit: 50,
      });
      setTraces(response.traces);
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

  const handleSelectTrace = async (traceId: string, around?: { from: string; to: string }) => {
    setSelectedTraceId(traceId);
    setSelectedSpans(null);
    setIsLoadingTrace(true);
    try {
      const response = await traceApi.getTrace(
        projectId,
        traceId,
        around?.from ?? (timeRange ? timeRange.from.toISOString() : undefined),
        around?.to ?? (timeRange ? timeRange.to.toISOString() : undefined),
      );
      setSelectedSpans(response.spans);
    } catch (e) {
      message.error((e as Error).message);
    } finally {
      setIsLoadingTrace(false);
    }
  };

  // A trace opened from a log: read it from an hour either side of the
  // log, not the picker's range (the log may be older than it).
  useEffect(() => {
    if (!initialTrace) return;
    const at = dayjs(initialTrace.at);
    void handleSelectTrace(initialTrace.traceId, {
      from: at.subtract(1, 'hour').toISOString(),
      to: at.add(1, 'hour').toISOString(),
    });
  }, [initialTrace?.traceId, initialTrace?.at]);

  const columns: ColumnsType<TraceSummary> = [
    {
      title: 'Trace',
      dataIndex: 'rootName',
      render: (_, trace) => (
        <div>
          <div className="font-medium text-gray-800 dark:text-gray-100">{trace.rootName}</div>
          <div className="text-xs text-gray-400 dark:text-gray-400">
            {trace.traceId.slice(0, 16)}…
          </div>
        </div>
      ),
    },
    { title: 'Root service', dataIndex: 'rootServiceName' },
    {
      title: 'Services',
      dataIndex: 'services',
      render: (services: string[]) => (
        <div className="flex flex-wrap gap-1">
          {services.map((s) => (
            <Tag key={s}>{s}</Tag>
          ))}
        </div>
      ),
    },
    {
      title: 'Start time',
      dataIndex: 'startTime',
      render: (value: string) => dayjs(value).format('YYYY-MM-DD HH:mm:ss.SSS'),
    },
    {
      title: 'Duration',
      dataIndex: 'durationMs',
      render: (value: number) => formatDurationMs(value),
      sorter: (a, b) => a.durationMs - b.durationMs,
    },
    { title: 'Spans', dataIndex: 'spanCount', width: 80 },
    {
      title: 'Errors',
      dataIndex: 'errorCount',
      width: 90,
      render: (value: number) =>
        value > 0 ? <Tag color="red">{value}</Tag> : <span className="text-gray-300">0</span>,
    },
  ];

  return (
    <div
      className="grow overflow-auto rounded bg-white p-4 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <div className="mb-4 flex flex-wrap items-end gap-3">
        <TimeRangePickerComponent onChange={setTimeRange} />

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Service
          </label>
          <Select
            allowClear
            showSearch
            value={serviceName}
            onChange={setServiceName}
            className="w-48"
            placeholder="Any service"
            options={services.map((s) => ({ value: s, label: s }))}
          />
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Operation
          </label>
          <Select
            allowClear
            showSearch
            value={operationName}
            onChange={setOperationName}
            className="w-48"
            placeholder={serviceName ? 'Any operation' : 'Pick a service first'}
            disabled={!serviceName}
            options={operations.map((o) => ({ value: o, label: o }))}
          />
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Min duration (ms)
          </label>
          <InputNumber
            min={0}
            value={minDurationMs}
            onChange={(v) => setMinDurationMs(v ?? undefined)}
          />
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Max duration (ms)
          </label>
          <InputNumber
            min={0}
            value={maxDurationMs}
            onChange={(v) => setMaxDurationMs(v ?? undefined)}
          />
        </div>

        <Checkbox checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)}>
          Errors only
        </Checkbox>

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

      {hasSearched && traces.length === 0 && !isSearching && (
        <Empty description="No traces found in this time range" />
      )}

      {traces.length > 0 && (
        <Table<TraceSummary>
          size="small"
          rowKey="traceId"
          dataSource={traces}
          columns={columns}
          loading={isSearching}
          pagination={false}
          onRow={(trace) => ({
            className: 'cursor-pointer',
            onClick: () => handleSelectTrace(trace.traceId),
          })}
        />
      )}

      <Drawer
        title={selectedTraceId ? `Trace ${selectedTraceId.slice(0, 16)}…` : 'Trace'}
        open={!!selectedTraceId}
        onClose={() => {
          setSelectedTraceId(null);
          setSelectedSpans(null);
        }}
        width="min(90vw, 900px)"
      >
        {isLoadingTrace || !selectedSpans ? (
          <div className="flex justify-center py-8">
            <Spin />
          </div>
        ) : (
          <TraceWaterfallComponent spans={selectedSpans} />
        )}
      </Drawer>
    </div>
  );
};
