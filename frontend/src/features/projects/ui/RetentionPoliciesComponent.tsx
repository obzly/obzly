import { DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, InputNumber, Select, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type {
  LogSampleRule,
  RetentionPolicy,
  RetentionSignal,
} from '../../../entity/projects/model/RetentionPolicy';

interface Props {
  projectId: string;
  canEdit: boolean;
  // The project's log retention, which signals without their own use.
  logRetentionDays: number;
}

const LEVELS = ['', 'trace', 'debug', 'info', 'warn', 'error', 'fatal'];

const percent = (rate: number) => Math.round(rate * 1000) / 10;

export const RetentionPoliciesComponent = ({ projectId, canEdit, logRetentionDays }: Props) => {
  const { message } = App.useApp();
  const [saved, setSaved] = useState<RetentionPolicy[]>([]);
  const [policies, setPolicies] = useState<RetentionPolicy[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  const load = async () => {
    setIsLoading(true);
    try {
      const loaded = await projectApi.getRetentionPolicies(projectId);
      setSaved(loaded);
      setPolicies(loaded);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to load retention policies');
    } finally {
      setIsLoading(false);
    }
  };

  const update = (signal: RetentionSignal, changes: Partial<RetentionPolicy>) => {
    setPolicies((prev) => prev.map((p) => (p.signal === signal ? { ...p, ...changes } : p)));
  };

  const updateRule = (index: number, changes: Partial<LogSampleRule>) => {
    const rules = [...(policyOf('logs')?.sampleRules ?? [])];
    rules[index] = { ...rules[index], ...changes };
    update('logs', { sampleRules: rules });
  };

  const save = async () => {
    setIsSaving(true);
    try {
      const updated = await projectApi.setRetentionPolicies(projectId, policies);
      setSaved(updated);
      setPolicies(updated);
      message.success('Retention and sampling updated');
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to save retention policies');
    } finally {
      setIsSaving(false);
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  const policyOf = (signal: RetentionSignal) => policies.find((p) => p.signal === signal);
  const isChanged = JSON.stringify(saved) !== JSON.stringify(policies);
  const traces = policyOf('traces');
  const metrics = policyOf('metrics');
  const logs = policyOf('logs');

  const daysField = (signal: RetentionSignal, label: string, help: string) => (
    <div>
      <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">{label}</div>
      <InputNumber
        value={policyOf(signal)?.retentionDays}
        onChange={(value) => update(signal, { retentionDays: value ?? 0 })}
        disabled={!canEdit}
        min={0}
        max={3650}
        style={{ width: '150px' }}
      />
      <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">{help}</div>
    </div>
  );

  if (isLoading || !traces || !metrics || !logs) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  return (
    <div>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        {daysField('traces', 'Trace retention (days)', `0: like logs (${logRetentionDays} days)`)}
        {daysField('metrics', 'Metric retention (days)', `0: like logs (${logRetentionDays} days)`)}
        <div>
          <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
            Metric rollup retention (days)
          </div>
          <InputNumber
            value={metrics.rollupDays}
            onChange={(value) => update('metrics', { rollupDays: value ?? 0 })}
            disabled={!canEdit}
            min={0}
            max={3650}
            style={{ width: '150px' }}
          />
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
            Keep 5-minute and 1-hour rollups this long after raw samples are deleted (0: delete them
            together). Charts of older data show fewer points.
          </div>
        </div>
        <div>
          <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
            Trace sampling (% kept)
          </div>
          <InputNumber
            value={percent(traces.sampleRate)}
            onChange={(value) => update('traces', { sampleRate: (value ?? 100) / 100 })}
            disabled={!canEdit}
            min={0}
            max={100}
            step={1}
            addonAfter="%"
            style={{ width: '150px' }}
          />
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
            Traces with an error are always kept. Applied once a day&apos;s spans are compacted, so
            the last day stays complete.
          </div>
        </div>
        <div>
          <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
            Always keep traces slower than (ms)
          </div>
          <InputNumber
            value={traces.slowMs}
            onChange={(value) => update('traces', { slowMs: value ?? 0 })}
            disabled={!canEdit}
            min={0}
            style={{ width: '150px' }}
          />
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">0: no slow-trace rule</div>
        </div>
      </div>

      <div className="mt-4">
        <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">Log sampling rules</div>
        <div className="mb-2 text-xs text-gray-500 dark:text-gray-300">
          Keep a share of matching logs at ingest; the first matching rule wins and unmatched logs
          are all kept. Logs of the same trace are kept or dropped together.
        </div>
        {logs.sampleRules.map((rule, index) => (
          <div key={index} className="mb-2 flex items-center gap-2">
            <Input
              placeholder="Any service"
              value={rule.service}
              onChange={(e) => updateRule(index, { service: e.target.value })}
              disabled={!canEdit}
              style={{ width: '180px' }}
            />
            <Select
              value={rule.level}
              onChange={(value) => updateRule(index, { level: value })}
              disabled={!canEdit}
              style={{ width: '120px' }}
              options={LEVELS.map((level) => ({ value: level, label: level || 'Any level' }))}
            />
            <InputNumber
              value={percent(rule.rate)}
              onChange={(value) => updateRule(index, { rate: (value ?? 100) / 100 })}
              disabled={!canEdit}
              min={0}
              max={100}
              addonAfter="% kept"
              style={{ width: '150px' }}
            />
            {canEdit && (
              <Button
                icon={<DeleteOutlined />}
                onClick={() =>
                  update('logs', { sampleRules: logs.sampleRules.filter((_, i) => i !== index) })
                }
              />
            )}
          </div>
        ))}
        {canEdit && (
          <Button
            icon={<PlusOutlined />}
            size="small"
            onClick={() =>
              update('logs', {
                sampleRules: [...logs.sampleRules, { service: '', level: 'debug', rate: 0.1 }],
              })
            }
          >
            Add rule
          </Button>
        )}
      </div>

      {isChanged && canEdit && (
        <div className="mt-4 flex space-x-2">
          <Button
            type="primary"
            onClick={save}
            loading={isSaving}
            disabled={isSaving}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            {isSaving ? 'Saving...' : 'Save Changes'}
          </Button>
          <Button type="default" onClick={() => setPolicies(saved)} disabled={isSaving}>
            Cancel
          </Button>
        </div>
      )}
    </div>
  );
};
