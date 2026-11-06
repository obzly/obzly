import { App, Button, Checkbox, Drawer, Input, InputNumber, Select, Table, Tag } from 'antd';
import { useEffect, useState } from 'react';

import {
  type AlertRule,
  type Comparison,
  type NotificationChannel,
  type PreviewSeries,
  type RuleRequest,
  type RuleSource,
  alertingApi,
} from '../../../entity/alerting';
import { type QueryNode, type QueryableField, queryApi } from '../../../entity/query';
import { QueryBuilderComponent } from '../../query';
import { LabelTags } from './labels';
import { errorMessage } from './lib';

interface Props {
  projectId: string;
  rule: AlertRule | null;
  channels: NotificationChannel[];
  canEdit: boolean;
  onClose: () => void;
  onSaved: (rule: AlertRule) => void;
}

const COMPARISONS: Comparison[] = ['>', '>=', '<', '<=', '==', '!='];
const TRACE_METRICS = [
  'count',
  'error_count',
  'avg_duration_ms',
  'p50_duration_ms',
  'p95_duration_ms',
  'p99_duration_ms',
];
const TRACE_GROUPS = ['', 'service_name', 'name', 'status_code', 'kind'];

const emptyRule: RuleRequest = {
  name: '',
  enabled: true,
  source: 'metrics',
  query: { promql: '' },
  comparison: '>',
  threshold: 0,
  windowSeconds: 300,
  intervalSeconds: 60,
  forSeconds: 0,
  repeatSeconds: 4 * 3600,
  labels: {},
  summary: '',
  channelIds: [],
};

const fieldLabel = (text: string, help?: string) => (
  <div className="mb-1">
    <span className="font-medium text-gray-900 dark:text-gray-100">{text}</span>
    {help && <div className="text-xs text-gray-500 dark:text-gray-300">{help}</div>}
  </div>
);

// A new log patterns rule watches error logs.
const ERROR_LOGS: QueryNode = {
  type: 'condition',
  condition: { field: 'level', operator: 'equals', value: 'error' },
};

export const RuleEditorDrawerComponent = ({
  projectId,
  rule,
  channels,
  canEdit,
  onClose,
  onSaved,
}: Props) => {
  const { message } = App.useApp();
  const [draft, setDraft] = useState<RuleRequest>(
    rule
      ? {
          ...emptyRule,
          ...rule,
        }
      : emptyRule,
  );
  const [fields, setFields] = useState<QueryableField[]>([]);
  const [preview, setPreview] = useState<PreviewSeries[] | null>(null);
  const [isPreviewing, setIsPreviewing] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [labelsText, setLabelsText] = useState(
    Object.entries(draft.labels)
      .map(([k, v]) => `${k}=${v}`)
      .join(', '),
  );

  const set = (changes: Partial<RuleRequest>) => setDraft((prev) => ({ ...prev, ...changes }));
  const setQuery = (changes: Partial<RuleRequest['query']>) =>
    setDraft((prev) => ({ ...prev, query: { ...prev.query, ...changes } }));

  const parsedLabels = (): Record<string, string> => {
    const labels: Record<string, string> = {};
    for (const pair of labelsText.split(',')) {
      const [key, ...rest] = pair.split('=');
      if (key.trim()) {
        labels[key.trim()] = rest.join('=').trim();
      }
    }
    return labels;
  };

  const request = (): RuleRequest => ({ ...draft, labels: parsedLabels() });

  const runPreview = async () => {
    setIsPreviewing(true);
    try {
      setPreview(await alertingApi.previewRule(projectId, request()));
    } catch (error: unknown) {
      setPreview(null);
      message.error(errorMessage(error, 'Preview failed'));
    } finally {
      setIsPreviewing(false);
    }
  };

  const save = async () => {
    setIsSaving(true);
    try {
      const saved = rule
        ? await alertingApi.updateRule(projectId, rule.id, request())
        : await alertingApi.createRule(projectId, request());
      message.success(rule ? 'Rule updated' : 'Rule created');
      onSaved(saved);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to save rule'));
    } finally {
      setIsSaving(false);
    }
  };

  useEffect(() => {
    if (draft.source === 'logs' && fields.length === 0) {
      queryApi
        .getQueryableFields(projectId)
        .then((response) => setFields(response.fields))
        .catch(() => setFields([]));
    }
  }, [draft.source]);

  const changeSource = (source: RuleSource) => {
    setPreview(null);
    set({
      source,
      query:
        source === 'metrics'
          ? { promql: '' }
          : source === 'logs'
            ? {}
            : source === 'log_patterns'
              ? { logs: ERROR_LOGS, baselineSeconds: 86400, minCount: 5 }
              : { metric: 'error_count' },
      // A pattern rule's value is its rate over the baseline's: new
      // patterns, and ones that tripled.
      ...(source === 'log_patterns' ? { comparison: '>=' as const, threshold: 3 } : {}),
    });
  };

  return (
    <Drawer
      open
      width={720}
      title={rule ? (canEdit ? `Edit ${rule.name}` : rule.name) : 'New alert rule'}
      onClose={onClose}
      extra={
        canEdit && (
          <Button
            type="primary"
            onClick={save}
            loading={isSaving}
            disabled={!draft.name.trim()}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Save
          </Button>
        )
      }
    >
      <fieldset disabled={!canEdit} className="space-y-4">
        <div>
          {fieldLabel('Name')}
          <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
        <div>
          {fieldLabel('Data')}
          <Select
            value={draft.source}
            onChange={changeSource}
            style={{ width: 200 }}
            options={[
              { value: 'metrics', label: 'Metrics (PromQL)' },
              { value: 'logs', label: 'Logs (count)' },
              { value: 'log_patterns', label: 'Log patterns (new or spiking)' },
              { value: 'traces', label: 'Traces' },
            ]}
          />
        </div>

        {draft.source === 'metrics' && (
          <div>
            {fieldLabel(
              'PromQL',
              'An instant query; every series it returns is checked, e.g. sum by (service) (rate(http_errors_total[5m]))',
            )}
            <Input.TextArea
              rows={3}
              className="font-mono"
              value={draft.query.promql}
              onChange={(e) => setQuery({ promql: e.target.value })}
            />
          </div>
        )}

        {draft.source === 'logs' && (
          <>
            <div>
              {fieldLabel('Logs to count', 'Leave empty to count every log')}
              <QueryBuilderComponent
                fields={fields}
                query={(draft.query.logs as QueryNode | undefined) ?? null}
                onChange={(logs) => setQuery({ logs: logs ?? undefined })}
              />
            </div>
            <div>
              {fieldLabel('Per field (optional)', 'One alert per value, e.g. service_name')}
              <Input
                value={draft.query.groupBy}
                onChange={(e) => setQuery({ groupBy: e.target.value })}
                style={{ width: 260 }}
              />
            </div>
          </>
        )}

        {draft.source === 'log_patterns' && (
          <>
            <div className="text-xs text-gray-500 dark:text-gray-400">
              One alert per message pattern that is new (not seen in the baseline) or whose rate
              rose to at least the threshold times the baseline&apos;s - e.g. 3 fires when a pattern
              triples.
            </div>
            <div>
              {fieldLabel('Logs to watch', 'Usually errors; leave empty to watch every log')}
              <QueryBuilderComponent
                fields={fields}
                query={(draft.query.logs as QueryNode | undefined) ?? null}
                onChange={(logs) => setQuery({ logs: logs ?? undefined })}
              />
            </div>
            <div className="flex flex-wrap gap-3">
              <div>
                {fieldLabel('Compared with the', 'The time just before the window')}
                <Select
                  value={draft.query.baselineSeconds ?? 86400}
                  onChange={(baselineSeconds) => setQuery({ baselineSeconds })}
                  style={{ width: 160 }}
                  options={[
                    { value: 3600, label: 'hour before' },
                    { value: 6 * 3600, label: '6 hours before' },
                    { value: 86400, label: 'day before' },
                    { value: 7 * 86400, label: 'week before' },
                  ]}
                />
              </div>
              <div>
                {fieldLabel('At least (logs)', 'Ignore patterns with fewer logs in the window')}
                <InputNumber
                  value={draft.query.minCount ?? 5}
                  min={1}
                  onChange={(minCount) => setQuery({ minCount: minCount ?? 5 })}
                />
              </div>
            </div>
          </>
        )}

        {draft.source === 'traces' && (
          <div className="grid grid-cols-2 gap-3">
            <div>
              {fieldLabel('Measure')}
              <Select
                value={draft.query.metric}
                onChange={(metric) => setQuery({ metric })}
                style={{ width: '100%' }}
                options={TRACE_METRICS.map((m) => ({ value: m, label: m }))}
              />
            </div>
            <div>
              {fieldLabel('Per (optional)')}
              <Select
                value={draft.query.groupBy ?? ''}
                onChange={(groupBy) => setQuery({ groupBy })}
                style={{ width: '100%' }}
                options={TRACE_GROUPS.map((g) => ({ value: g, label: g || 'nothing' }))}
              />
            </div>
            <div>
              {fieldLabel('Service (optional)')}
              <Input
                value={draft.query.serviceName}
                onChange={(e) => setQuery({ serviceName: e.target.value })}
              />
            </div>
            <div>
              {fieldLabel('Operation (optional)')}
              <Input
                value={draft.query.operationName}
                onChange={(e) => setQuery({ operationName: e.target.value })}
              />
            </div>
            <Checkbox
              checked={draft.query.rootSpansOnly}
              onChange={(e) => setQuery({ rootSpansOnly: e.target.checked })}
            >
              Requests only (root spans)
            </Checkbox>
          </div>
        )}

        <div className="flex flex-wrap items-end gap-3">
          <div>
            {fieldLabel('Fire when value')}
            <Select
              value={draft.comparison}
              onChange={(comparison) => set({ comparison })}
              style={{ width: 80 }}
              options={COMPARISONS.map((c) => ({ value: c, label: c }))}
            />
          </div>
          <div>
            {fieldLabel('Threshold')}
            <InputNumber
              value={draft.threshold}
              onChange={(threshold) => set({ threshold: threshold ?? 0 })}
            />
          </div>
          {draft.source !== 'metrics' && (
            <div>
              {fieldLabel('Over the last (s)')}
              <InputNumber
                value={draft.windowSeconds}
                min={60}
                max={86400}
                onChange={(windowSeconds) => set({ windowSeconds: windowSeconds ?? 300 })}
              />
            </div>
          )}
          <Button onClick={runPreview} loading={isPreviewing}>
            Preview
          </Button>
        </div>
        {preview && (
          <Table
            size="small"
            rowKey={(series) => JSON.stringify(series.labels)}
            dataSource={preview}
            pagination={{ pageSize: 10, hideOnSinglePage: true }}
            locale={{ emptyText: 'The query returns nothing right now' }}
            columns={[
              {
                title: 'Series',
                render: (_, series) => <LabelTags labels={series.labels} />,
              },
              {
                title: 'Value',
                width: 120,
                render: (_, series) => Number(series.value.toPrecision(6)),
              },
              {
                title: '',
                width: 90,
                render: (_, series) =>
                  series.firing ? <Tag color="red">would fire</Tag> : <Tag>ok</Tag>,
              },
            ]}
          />
        )}

        <div className="grid grid-cols-3 gap-3">
          <div>
            {fieldLabel('Check every (s)')}
            <InputNumber
              value={draft.intervalSeconds}
              min={15}
              max={86400}
              onChange={(intervalSeconds) => set({ intervalSeconds: intervalSeconds ?? 60 })}
            />
          </div>
          <div>
            {fieldLabel('For (s)', 'How long before it fires')}
            <InputNumber
              value={draft.forSeconds}
              min={0}
              max={86400}
              onChange={(forSeconds) => set({ forSeconds: forSeconds ?? 0 })}
            />
          </div>
          <div>
            {fieldLabel('Remind every (s)', '0: once')}
            <InputNumber
              value={draft.repeatSeconds}
              min={0}
              max={604800}
              onChange={(repeatSeconds) => set({ repeatSeconds: repeatSeconds ?? 0 })}
            />
          </div>
        </div>

        <div>
          {fieldLabel('Notify')}
          <Select
            mode="multiple"
            value={draft.channelIds}
            onChange={(channelIds) => set({ channelIds })}
            style={{ width: '100%' }}
            placeholder={channels.length === 0 ? 'Add a channel in the Channels tab' : 'Channels'}
            options={channels.map((c) => ({ value: c.id, label: `${c.name} (${c.kind})` }))}
          />
        </div>
        <div>
          {fieldLabel('Summary', 'Included in every notification')}
          <Input.TextArea
            rows={2}
            value={draft.summary}
            onChange={(e) => set({ summary: e.target.value })}
          />
        </div>
        <div>
          {fieldLabel('Labels', 'Added to every alert, e.g. severity=page, team=core')}
          <Input value={labelsText} onChange={(e) => setLabelsText(e.target.value)} />
        </div>
      </fieldset>
    </Drawer>
  );
};
