import { DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, InputNumber, Select, Spin } from 'antd';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type {
  LogSampleRule,
  RetentionPolicy,
  RetentionSignal,
} from '../../../entity/projects/model/RetentionPolicy';
import { RankedList, type RankedRow } from '../../../shared/ui/v2';
import { SaveBar } from './ProjectPageShell';

interface Props {
  projectId: string;
  canEdit: boolean;
  // The project's log retention, which signals without their own use.
  logRetentionDays: number;
  // Stored size per signal from the stats endpoints; undefined while loading or failed.
  storageMb?: { logs?: number; traces?: number; metrics?: number };
  // The part of storageMb already in object storage; the rest is fresh segments on local disk.
  objectStorageMb?: { logs?: number; traces?: number; metrics?: number };
}

interface StorageSplitRow {
  key: string;
  label: string;
  totalMb?: number;
  objectMb: number;
  days: number;
  color: string;
}

const LEVELS = ['', 'trace', 'debug', 'info', 'warn', 'error', 'fatal'];

const percent = (rate: number) => Math.round(rate * 1000) / 10;

const formatMb = (mb: number) =>
  mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(mb >= 10 ? 0 : 2)} MB`;

const FIELD_LABEL = 'text-[13px] font-medium text-gray-700 dark:text-gray-200';
const FIELD_HELP = 'text-xs text-gray-500 dark:text-gray-400';

export const RetentionPoliciesComponent = ({
  projectId,
  canEdit,
  logRetentionDays,
  storageMb,
  objectStorageMb,
}: Props) => {
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
    <div className="flex flex-col gap-1">
      <div className={FIELD_LABEL}>{label}</div>
      <InputNumber
        value={policyOf(signal)?.retentionDays}
        onChange={(value) => update(signal, { retentionDays: value ?? 0 })}
        disabled={!canEdit}
        min={0}
        max={3650}
        className="!font-mono"
        style={{ width: '150px' }}
      />
      <div className={FIELD_HELP}>{help}</div>
    </div>
  );

  // In effect now, so from the saved policies rather than unsaved edits.
  const savedDays = (signal: RetentionSignal) =>
    saved.find((p) => p.signal === signal)?.retentionDays || logRetentionDays;
  const storageRows: RankedRow[] = storageMb
    ? (
        [
          ['logs', 'Logs', storageMb.logs, logRetentionDays, 'var(--v2-accent)'],
          ['traces', 'Traces', storageMb.traces, savedDays('traces'), 'var(--v2-blue)'],
          ['metrics', 'Metrics', storageMb.metrics, savedDays('metrics'), 'var(--v2-purple)'],
        ] as const
      ).map(([key, label, mb, days, color]) => ({
        key,
        label,
        value: mb ?? 0,
        display: mb === undefined ? 'no data' : formatMb(mb),
        extra: `${days} days`,
        color,
      }))
    : [];

  // Shown only once something is in object storage: until then (or without
  // object storage) every byte is on local disk and the plain bars say it all.
  const splitRows: StorageSplitRow[] =
    storageMb && objectStorageMb && Object.values(objectStorageMb).some((mb) => (mb ?? 0) > 0)
      ? storageRows.map((row) => ({
          key: row.key,
          label: String(row.label),
          totalMb: storageMb[row.key as keyof typeof storageMb],
          objectMb: objectStorageMb[row.key as keyof typeof objectStorageMb] ?? 0,
          days: row.key === 'logs' ? logRetentionDays : savedDays(row.key as RetentionSignal),
          color: row.color ?? 'var(--v2-accent)',
        }))
      : [];

  if (isLoading || !traces || !metrics || !logs) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  return (
    <div>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3">
        {daysField('traces', 'Trace retention (days)', `0: like logs (${logRetentionDays} days)`)}
        {daysField('metrics', 'Metric retention (days)', `0: like logs (${logRetentionDays} days)`)}
        <div className="flex flex-col gap-1">
          <div className={FIELD_LABEL}>Metric rollup retention (days)</div>
          <InputNumber
            value={metrics.rollupDays}
            onChange={(value) => update('metrics', { rollupDays: value ?? 0 })}
            disabled={!canEdit}
            min={0}
            max={3650}
            style={{ width: '150px' }}
          />
          <div className={FIELD_HELP}>
            Keep 5-minute and 1-hour rollups this long after raw samples are deleted (0: delete them
            together). Charts of older data show fewer points.
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <div className={FIELD_LABEL}>Trace sampling (% kept)</div>
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
          <div className={FIELD_HELP}>
            Traces with an error are always kept. Applied once a day&apos;s spans are compacted, so
            the last day stays complete.
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <div className={FIELD_LABEL}>Always keep traces slower than (ms)</div>
          <InputNumber
            value={traces.slowMs}
            onChange={(value) => update('traces', { slowMs: value ?? 0 })}
            disabled={!canEdit}
            min={0}
            style={{ width: '150px' }}
          />
          <div className={FIELD_HELP}>0: no slow-trace rule</div>
        </div>
      </div>

      <div className="mt-4 flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
        <strong className="text-sm font-semibold text-gray-900 dark:text-gray-50">
          Log sampling rules
        </strong>
        <div className={FIELD_HELP}>
          Keep a share of matching logs at ingest; the first matching rule wins and unmatched logs
          are all kept. Logs of the same trace are kept or dropped together.
        </div>
        {logs.sampleRules.map((rule, index) => (
          <div key={index} className="flex flex-wrap items-center gap-2">
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
            {rule.pattern && (
              <span
                className="max-w-80 truncate rounded-md bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-600 dark:bg-gray-700 dark:text-gray-300"
                title={`Only logs with this message pattern (made on Logs › Volume & noise)\n${rule.patternText || rule.pattern}`}
              >
                pattern = {rule.patternText || rule.pattern}
              </span>
            )}
            {canEdit && (
              <Button
                type="text"
                aria-label="Delete rule"
                title="Delete rule"
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
            className="self-start !border-dashed"
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

      {storageRows.length > 0 && (
        <div className="mt-4 flex flex-col gap-3 border-t border-gray-200 pt-3 dark:border-gray-700">
          <span className="flex flex-wrap items-baseline justify-between gap-2">
            <strong className="text-sm font-semibold text-gray-900 dark:text-gray-50">
              Storage today
            </strong>
            <span className={FIELD_HELP}>Stored size and how long each signal is kept</span>
          </span>
          {splitRows.length > 0 ? (
            <StorageSplitList rows={splitRows} />
          ) : (
            <RankedList rows={storageRows} />
          )}
        </div>
      )}

      {isChanged && canEdit && (
        <SaveBar>
          <Button size="small" onClick={() => setPolicies(saved)} disabled={isSaving}>
            Cancel
          </Button>
          <Button size="small" type="primary" onClick={save} loading={isSaving} disabled={isSaving}>
            {isSaving ? 'Saving...' : 'Save Changes'}
          </Button>
        </SaveBar>
      )}
    </div>
  );
};

/**
 * Storage bars split by tier: solid is fresh segments still only on local
 * disk (uploaded once compaction merges them), faded is object storage.
 */
const StorageSplitList = ({ rows }: { rows: StorageSplitRow[] }) => {
  const max = Math.max(...rows.map((r) => r.totalMb ?? 0)) || 1;
  return (
    <div className="flex flex-col gap-3">
      {rows.map((r) => {
        const total = r.totalMb ?? 0;
        const local = Math.max(0, total - r.objectMb);
        return (
          <div key={r.key} className="flex flex-col gap-1.5">
            <span className="flex justify-between gap-3 text-sm">
              <span className="min-w-0 truncate text-gray-800 dark:text-gray-100">{r.label}</span>
              <span className="whitespace-nowrap text-gray-900 dark:text-gray-50">
                <strong className="font-semibold">
                  {r.totalMb === undefined ? 'no data' : formatMb(total)}
                </strong>
                <span className="ml-1.5 text-xs text-gray-500 dark:text-gray-400">
                  {r.days} days
                </span>
              </span>
            </span>
            <span
              className="flex h-1.5 overflow-hidden rounded-full bg-[var(--v2-track)]"
              role="img"
              aria-label={`${r.label}: ${formatMb(local)} on local disk, ${formatMb(r.objectMb)} in object storage`}
            >
              <span
                className="block h-full"
                style={{ width: `${(local / max) * 100}%`, background: r.color }}
              />
              <span
                className="block h-full opacity-40"
                style={{ width: `${(r.objectMb / max) * 100}%`, background: r.color }}
              />
            </span>
            {r.totalMb !== undefined && (
              <span className={FIELD_HELP}>
                {formatMb(local)} fresh on local disk · {formatMb(r.objectMb)} in object storage
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
};
