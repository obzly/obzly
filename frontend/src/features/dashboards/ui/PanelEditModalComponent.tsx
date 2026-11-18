import { Alert, App, Checkbox, Input, InputNumber, Modal, Segmented, Select, Tag } from 'antd';
import React, { useEffect, useState } from 'react';

import {
  type Panel,
  type PanelHigherIs,
  type PanelSource,
  type PanelType,
  type SpanMetric,
  effectivePanelSource,
} from '../../../entity/dashboards';
import { metricApi } from '../../../entity/metrics';
import { type QueryNode, type QueryableField, queryApi } from '../../../entity/query';
import { traceApi } from '../../../entity/traces';
import { QueryBuilderComponent } from '../../query';
import {
  PANEL_TYPE_OPTIONS,
  SOURCE_OPTIONS,
  SPAN_GROUP_FIELD_OPTIONS,
  SPAN_METRIC_OPTIONS,
} from '../lib/panelDescription';
import {
  groupsIntoDiscreteValues as groupsIntoDiscreteValuesFor,
  isTimeBucketedType,
  kindUnavailableReason,
  settingsForType,
  usesHigherIs,
  usesRange,
  usesThresholds,
  validateSettings,
} from '../lib/panelKinds';

interface Props {
  projectId: string;
  panel: Panel | null; // null = creating a new panel
  // Chart type preselected for a new panel (the "Add a widget" picker).
  initialType?: PanelType;
  onSave: (panel: Panel) => void;
  onCancel: () => void;
}

const BUCKET_OPTIONS = [
  { value: 0, label: 'Auto' },
  { value: 60_000, label: '1 minute' },
  { value: 5 * 60_000, label: '5 minutes' },
  { value: 15 * 60_000, label: '15 minutes' },
  { value: 60 * 60_000, label: '1 hour' },
  { value: 24 * 60 * 60_000, label: '1 day' },
];

const UNIT_OPTIONS: { value: '' | 'bytes' | 'percent'; label: string }[] = [
  { value: '', label: 'None' },
  { value: 'bytes', label: 'Bytes' },
  { value: 'percent', label: 'Percent (0-1 fraction)' },
];

const HIGHER_IS_OPTIONS: { value: '' | PanelHigherIs; label: string }[] = [
  { value: '', label: 'Not set' },
  { value: 'good', label: 'Higher is good' },
  { value: 'bad', label: 'Higher is bad' },
];

const fieldLabel = 'mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200';

export const PanelEditModalComponent = ({
  projectId,
  panel,
  initialType,
  onSave,
  onCancel,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [fields, setFields] = useState<QueryableField[]>([]);
  const [services, setServices] = useState<string[]>([]);
  const [operations, setOperations] = useState<string[]>([]);
  const [metricNames, setMetricNames] = useState<string[]>([]);

  const [title, setTitle] = useState(panel?.title ?? '');
  const [type, setType] = useState<PanelType>(panel?.type ?? initialType ?? 'line');
  const [source, setSource] = useState<PanelSource>(panel ? effectivePanelSource(panel) : 'logs');

  const [query, setQuery] = useState<QueryNode | null>(panel?.query ?? null);

  const [serviceName, setServiceName] = useState<string | undefined>(panel?.serviceName);
  const [operationName, setOperationName] = useState<string | undefined>(panel?.operationName);
  const [rootSpansOnly, setRootSpansOnly] = useState(panel?.rootSpansOnly ?? false);
  const [metric, setMetric] = useState<SpanMetric>(panel?.metric ?? 'count');

  const [promQuery, setPromQuery] = useState(panel?.promQuery ?? '');
  const [sql, setSql] = useState(panel?.sql ?? '');

  const [groupField, setGroupField] = useState<string | undefined>(panel?.groupField);
  const [bucketMs, setBucketMs] = useState<number>(panel?.bucketMs ?? 0);
  const [limit, setLimit] = useState<number | undefined>(panel?.limit);

  const [unit, setUnit] = useState<'' | 'bytes' | 'percent'>(panel?.unit ?? '');
  const [min, setMin] = useState<number | undefined>(panel?.min);
  const [max, setMax] = useState<number | undefined>(panel?.max);
  const [warn, setWarn] = useState<number | undefined>(panel?.thresholds?.warn);
  const [critical, setCritical] = useState<number | undefined>(panel?.thresholds?.critical);
  const [higherIs, setHigherIs] = useState<'' | PanelHigherIs>(panel?.higherIs ?? '');

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
  const isSql = source === 'logs_sql';
  // A SQL panel buckets time in its own SQL.
  const isTimeSeries = isTimeBucketedType(type) && !isSql;
  const groupsIntoDiscreteValues = groupsIntoDiscreteValuesFor(type);
  const typeUnavailable = kindUnavailableReason(type, source, isSpans ? metric : undefined);
  const settings = settingsForType(type, {
    min,
    max,
    thresholds: { warn, critical },
    higherIs: higherIs || undefined,
  });
  const typeLabel = PANEL_TYPE_OPTIONS.find((o) => o.value === type)?.label ?? type;

  const handleSave = () => {
    if (!title.trim()) {
      message.error('Panel title is required');
      return;
    }
    if (isMetrics && !promQuery.trim()) {
      message.error('A PromQL query is required');
      return;
    }
    if (isSql && !sql.trim()) {
      message.error('A SQL query is required');
      return;
    }
    if (!isSpans && !isMetrics && !isSql && groupsIntoDiscreteValues && !groupField) {
      message.error(
        type === 'pie' || type === 'table'
          ? 'Pie and table panels need a "group by" field to break down by'
          : `${typeLabel} panels need a "group by" field to break down by`,
      );
      return;
    }
    if (typeUnavailable) {
      message.error(`${typeLabel} can't show this: ${typeUnavailable}`);
      return;
    }
    const settingsError = validateSettings(type, settings, unit || undefined);
    if (settingsError) {
      message.error(settingsError);
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
      unit: unit || undefined,
      gridPos: panel?.gridPos ?? { x: 0, y: 0, w: 6, h: 8 },
      ...settings,
    };

    if (isMetrics) {
      onSave({ ...basePanel, promQuery: promQuery.trim() });
      return;
    }

    if (isSql) {
      onSave({
        ...basePanel,
        groupField: undefined,
        limit: type === 'rankedList' ? limit : undefined,
        sql: sql.trim(),
      });
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
        : {
            ...basePanel,
            query,
            // Set by Logs › Chart › Save to dashboard; this editor has no controls for them.
            measure: panel?.measure,
            measureField: panel?.measureField,
            topGroups: panel?.topGroups,
          },
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

        <div className="flex flex-wrap gap-4">
          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              Data source
            </label>
            <Segmented<PanelSource>
              value={source}
              onChange={setSource}
              options={SOURCE_OPTIONS}
              aria-label="Data source"
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              Chart type
            </label>
            <Select
              value={type}
              onChange={setType}
              options={PANEL_TYPE_OPTIONS.map(({ value, label }) => {
                const reason = kindUnavailableReason(value, source, isSpans ? metric : undefined);
                return {
                  value,
                  disabled: reason !== null && value !== type,
                  label: reason ? (
                    <span title={reason}>
                      {label}{' '}
                      <span className="text-xs text-gray-500 dark:text-gray-400">· {reason}</span>
                    </span>
                  ) : (
                    label
                  ),
                };
              })}
              className="w-52"
              popupMatchSelectWidth={false}
            />
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

        {typeUnavailable && (
          <Alert
            type="warning"
            showIcon
            title={`${typeLabel} can't show this source: ${typeUnavailable}. Pick another chart type or metric.`}
          />
        )}

        <div className="flex flex-wrap gap-4">
          <div>
            <label className={fieldLabel}>Unit</label>
            <Select value={unit} onChange={setUnit} options={UNIT_OPTIONS} className="w-52" />
          </div>

          {usesRange(type) && (
            <>
              <div>
                <label className={fieldLabel}>Gauge min</label>
                <InputNumber
                  value={min}
                  onChange={(value) => setMin(value ?? undefined)}
                  placeholder="0"
                  className="w-32"
                  aria-label="Gauge min"
                />
              </div>
              <div>
                <label className={fieldLabel}>Gauge max</label>
                <InputNumber
                  value={max}
                  onChange={(value) => setMax(value ?? undefined)}
                  placeholder={unit === 'percent' ? '1' : 'required'}
                  className="w-32"
                  aria-label="Gauge max"
                />
              </div>
            </>
          )}

          {usesThresholds(type) && (
            <>
              <div>
                <label className={fieldLabel}>Warn at</label>
                <InputNumber
                  value={warn}
                  onChange={(value) => setWarn(value ?? undefined)}
                  placeholder="optional"
                  className="w-32"
                  aria-label="Warn threshold"
                />
              </div>
              <div>
                <label className={fieldLabel}>Critical at</label>
                <InputNumber
                  value={critical}
                  onChange={(value) => setCritical(value ?? undefined)}
                  placeholder="optional"
                  className="w-32"
                  aria-label="Critical threshold"
                />
              </div>
            </>
          )}

          {usesHigherIs(type) && (
            <div>
              <label className={fieldLabel}>Direction</label>
              <Select
                value={higherIs}
                onChange={setHigherIs}
                options={HIGHER_IS_OPTIONS}
                className="w-40"
                aria-label="Direction"
              />
            </div>
          )}
        </div>

        {(usesThresholds(type) || usesHigherIs(type)) && (
          <div className="-mt-2 text-xs text-gray-500 dark:text-gray-300">
            {type === 'statTrend'
              ? 'Direction colours the change pill: green when the value moves the good way, red when it moves the bad way, grey when not set.'
              : 'Thresholds are raw values (a percent is a 0-1 fraction). A value at or past a threshold is breached: above it by default, below it when higher is good.'}
          </div>
        )}

        {isSql ? (
          <>
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                SQL query
              </label>
              <Input.TextArea
                value={sql}
                onChange={(e) => setSql(e.target.value)}
                placeholder="SELECT lower(level) AS level, count(*) AS lines FROM logs GROUP BY 1"
                rows={5}
                className="!font-mono"
                aria-label="SQL query"
              />
              <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                One SELECT over the <code>logs</code> table, as in Logs › SQL, run over the
                dashboard&apos;s time range. A line needs a time column and numbers; a ranked list
                or donut a label and a number; a single number shows the first number of the first
                row. Dashboard variables and filters are not applied to SQL.
              </div>
            </div>
            {type === 'rankedList' && (
              <div>
                <label className={fieldLabel}>Max rows shown (optional)</label>
                <InputNumber
                  min={1}
                  max={1000}
                  value={limit}
                  onChange={(value) => setLimit(value ?? undefined)}
                  className="w-32"
                  aria-label="Max rows shown"
                />
              </div>
            )}
          </>
        ) : isMetrics ? (
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
