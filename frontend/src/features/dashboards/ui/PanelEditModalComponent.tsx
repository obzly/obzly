import { App, Checkbox, Input, InputNumber, Modal, Select, Tag } from 'antd';
import React, { useEffect, useState } from 'react';

import {
  type Panel,
  type PanelSource,
  type PanelType,
  type SpanGroupField,
  type SpanMetric,
  effectivePanelSource,
} from '../../../entity/dashboards';
import { metricApi } from '../../../entity/metrics';
import { type QueryNode, type QueryableField, queryApi } from '../../../entity/query';
import { traceApi } from '../../../entity/traces';
import { QueryBuilderComponent } from '../../query';

interface Props {
  projectId: string;
  panel: Panel | null; // null = creating a new panel
  onSave: (panel: Panel) => void;
  onCancel: () => void;
}

const PANEL_TYPE_OPTIONS: { value: PanelType; label: string }[] = [
  { value: 'line', label: 'Line chart' },
  { value: 'bar', label: 'Bar chart' },
  { value: 'pie', label: 'Pie chart' },
  { value: 'stat', label: 'Single number' },
  { value: 'table', label: 'Table (top values)' },
];

const SOURCE_OPTIONS: { value: PanelSource; label: string }[] = [
  { value: 'logs', label: 'Logs' },
  { value: 'spans', label: 'Traces' },
  { value: 'metrics', label: 'Metrics' },
];

const SPAN_METRIC_OPTIONS: { value: SpanMetric; label: string }[] = [
  { value: 'count', label: 'Count' },
  { value: 'error_count', label: 'Error count' },
  { value: 'avg_duration_ms', label: 'Avg duration (ms)' },
  { value: 'p50_duration_ms', label: 'p50 duration (ms)' },
  { value: 'p95_duration_ms', label: 'p95 duration (ms)' },
  { value: 'p99_duration_ms', label: 'p99 duration (ms)' },
];

const SPAN_GROUP_FIELD_OPTIONS: { value: SpanGroupField; label: string }[] = [
  { value: 'service_name', label: 'Service' },
  { value: 'name', label: 'Operation' },
  { value: 'status_code', label: 'Status' },
  { value: 'kind', label: 'Kind' },
];

const BUCKET_OPTIONS = [
  { value: 0, label: 'Auto' },
  { value: 60_000, label: '1 minute' },
  { value: 5 * 60_000, label: '5 minutes' },
  { value: 15 * 60_000, label: '15 minutes' },
  { value: 60 * 60_000, label: '1 hour' },
  { value: 24 * 60 * 60_000, label: '1 day' },
];

export const PanelEditModalComponent = ({
  projectId,
  panel,
  onSave,
  onCancel,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [fields, setFields] = useState<QueryableField[]>([]);
  const [services, setServices] = useState<string[]>([]);
  const [operations, setOperations] = useState<string[]>([]);
  const [metricNames, setMetricNames] = useState<string[]>([]);

  const [title, setTitle] = useState(panel?.title ?? '');
  const [type, setType] = useState<PanelType>(panel?.type ?? 'line');
  const [source, setSource] = useState<PanelSource>(panel ? effectivePanelSource(panel) : 'logs');

  const [query, setQuery] = useState<QueryNode | null>(panel?.query ?? null);

  const [serviceName, setServiceName] = useState<string | undefined>(panel?.serviceName);
  const [operationName, setOperationName] = useState<string | undefined>(panel?.operationName);
  const [rootSpansOnly, setRootSpansOnly] = useState(panel?.rootSpansOnly ?? false);
  const [metric, setMetric] = useState<SpanMetric>(panel?.metric ?? 'count');

  const [promQuery, setPromQuery] = useState(panel?.promQuery ?? '');

  const [groupField, setGroupField] = useState<string | undefined>(panel?.groupField);
  const [bucketMs, setBucketMs] = useState<number>(panel?.bucketMs ?? 0);
  const [limit, setLimit] = useState<number | undefined>(panel?.limit);

  useEffect(() => {
    queryApi
      .getQueryableFields(projectId)
      .then((response) => setFields(response.fields))
      .catch((e: unknown) => message.error((e as Error).message));
    traceApi
      .listServices(projectId)
      .then((response) => setServices(response.services))
      .catch(() => {
        /* traces may not be in use for this project - not an error worth surfacing here */
      });
    metricApi
      .listMetricNames(projectId)
      .then((response) => setMetricNames(response.metrics.map((m) => m.name)))
      .catch(() => {
        /* metrics may not be in use for this project - not an error worth surfacing here */
      });
  }, [projectId]);

  useEffect(() => {
    if (!serviceName) {
      setOperations([]);
      return;
    }
    traceApi
      .listOperations(projectId, serviceName)
      .then((response) => setOperations(response.operations))
      .catch(() => setOperations([]));
  }, [projectId, serviceName]);

  const isSpans = source === 'spans';
  const isMetrics = source === 'metrics';
  const isTimeSeries = type === 'line' || type === 'bar';
  const groupsIntoDiscreteValues = type === 'pie' || type === 'table';

  const handleSave = () => {
    if (!title.trim()) {
      message.error('Panel title is required');
      return;
    }
    if (isMetrics && !promQuery.trim()) {
      message.error('A PromQL query is required');
      return;
    }
    if (!isSpans && !isMetrics && groupsIntoDiscreteValues && !groupField) {
      message.error('Pie and table panels need a "group by" field to break down by');
      return;
    }

    const basePanel = {
      id: panel?.id ?? crypto.randomUUID(),
      title: title.trim(),
      type,
      source,
      groupField: groupField || undefined,
      bucketMs: isTimeSeries && bucketMs > 0 ? bucketMs : undefined,
      limit,
      gridPos: panel?.gridPos ?? { x: 0, y: 0, w: 6, h: 8 },
    };

    if (isMetrics) {
      onSave({ ...basePanel, promQuery: promQuery.trim() });
      return;
    }

    onSave(
      isSpans
        ? {
            ...basePanel,
            serviceName: serviceName || undefined,
            operationName: operationName || undefined,
            rootSpansOnly,
            metric,
          }
        : { ...basePanel, query },
    );
  };

  return (
    <Modal
      open
      title={panel ? 'Edit panel' : 'Add panel'}
      onCancel={onCancel}
      onOk={handleSave}
      okText="Save"
      width={760}
      destroyOnHidden
    >
      <div className="space-y-4">
        <div>
          <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            Title
          </label>
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Panel title"
          />
        </div>

        <div className="flex gap-4">
          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              Data source
            </label>
            <Select value={source} onChange={setSource} options={SOURCE_OPTIONS} className="w-40" />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              Chart type
            </label>
            <Select value={type} onChange={setType} options={PANEL_TYPE_OPTIONS} className="w-52" />
          </div>

          {isTimeSeries && (
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                Time bucket
              </label>
              <Select
                value={bucketMs}
                onChange={setBucketMs}
                options={BUCKET_OPTIONS}
                className="w-40"
              />
            </div>
          )}
        </div>

        {isMetrics ? (
          <>
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                PromQL query
              </label>
              <Input.TextArea
                value={promQuery}
                onChange={(e) => setPromQuery(e.target.value)}
                placeholder="sum by (service) (rate(http_requests_total[5m]))"
                rows={3}
                className="!font-mono"
              />
              <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                Any PromQL expression, evaluated as a range query over the panel&apos;s time window.
                Click a metric name below to insert it.
              </div>
            </div>

            {metricNames.length > 0 && (
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Available metrics
                </label>
                <div className="flex flex-wrap gap-1">
                  {metricNames.map((name) => (
                    <Tag
                      key={name}
                      className="cursor-pointer"
                      onClick={() => setPromQuery((prev) => (prev ? `${prev}${name}` : name))}
                    >
                      {name}
                    </Tag>
                  ))}
                </div>
              </div>
            )}
          </>
        ) : isSpans ? (
          <>
            <div className="flex flex-wrap gap-4">
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Service (optional)
                </label>
                <Select
                  allowClear
                  showSearch
                  value={serviceName}
                  onChange={setServiceName}
                  className="w-52"
                  placeholder="Any service"
                  options={services.map((s) => ({ value: s, label: s }))}
                />
              </div>

              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Operation (optional)
                </label>
                <Select
                  allowClear
                  showSearch
                  value={operationName}
                  onChange={setOperationName}
                  className="w-52"
                  placeholder={serviceName ? 'Any operation' : 'Pick a service first'}
                  disabled={!serviceName}
                  options={operations.map((o) => ({ value: o, label: o }))}
                />
              </div>

              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Metric
                </label>
                <Select
                  value={metric}
                  onChange={setMetric}
                  options={SPAN_METRIC_OPTIONS}
                  className="w-52"
                />
              </div>

              <div className="flex items-end pb-1.5">
                <Checkbox
                  checked={rootSpansOnly}
                  onChange={(e) => setRootSpansOnly(e.target.checked)}
                >
                  Root spans only (requests, not internal spans)
                </Checkbox>
              </div>
            </div>

            <div className="flex gap-4">
              <div className="grow">
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Group by{groupsIntoDiscreteValues ? '' : ' (optional)'}
                </label>
                <Select
                  allowClear
                  value={groupField}
                  onChange={setGroupField}
                  className="w-full max-w-72"
                  placeholder="No grouping"
                  options={SPAN_GROUP_FIELD_OPTIONS}
                />
              </div>

              {(groupsIntoDiscreteValues || isTimeSeries) && (
                <div>
                  <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                    Max {groupsIntoDiscreteValues ? 'groups' : 'buckets'} (optional)
                  </label>
                  <InputNumber
                    min={1}
                    max={1000}
                    value={limit}
                    onChange={(value) => setLimit(value ?? undefined)}
                    className="w-32"
                  />
                </div>
              )}
            </div>
          </>
        ) : (
          <>
            <div className="flex gap-4">
              <div className="grow">
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  Group by field{groupsIntoDiscreteValues ? '' : ' (optional)'}
                </label>
                <Select
                  allowClear
                  showSearch
                  value={groupField}
                  onChange={setGroupField}
                  className="w-full max-w-72"
                  placeholder="No grouping"
                  options={fields.map((field) => ({ value: field.name, label: field.name }))}
                />
              </div>

              {(groupsIntoDiscreteValues || isTimeSeries) && (
                <div>
                  <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                    Max {groupsIntoDiscreteValues ? 'groups' : 'buckets'} (optional)
                  </label>
                  <InputNumber
                    min={1}
                    max={1000}
                    value={limit}
                    onChange={(value) => setLimit(value ?? undefined)}
                    className="w-32"
                  />
                </div>
              )}
            </div>

            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                Filter (optional)
              </label>
              <QueryBuilderComponent fields={fields} query={query} onChange={setQuery} />
            </div>
          </>
        )}
      </div>
    </Modal>
  );
};
