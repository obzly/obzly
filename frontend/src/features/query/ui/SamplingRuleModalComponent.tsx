import { App, Button, Modal } from 'antd';
import React, { useState } from 'react';

import { type LogSampleRule, projectApi } from '../../../entity/projects';
import {
  KEEP_CHOICES,
  formatBytes,
  formatCount,
  samplingPreview,
  withSampleRule,
} from '../lib/logInsights';
import { SegmentedControl } from './LogsViewTabs';

export interface SamplingTarget {
  hash: string;
  patternText: string;
  service: string;
  level: string;
  /** Lines per day today, for the preview; null when unknown. */
  perDay: number | null;
}

interface Props {
  projectId: string;
  target: SamplingTarget;
  /** Stored bytes per line (0: unknown). */
  bytesPerLine: number;
  initialKeep: number;
  onClose: () => void;
  onSaved: (rule: LogSampleRule) => void;
}

/**
 * A log sampling rule for one pattern of one service and level, added to the
 * project's Retention & sampling settings (first, so it wins over broader rules).
 */
export const SamplingRuleModalComponent = ({
  projectId,
  target,
  bytesPerLine,
  initialKeep,
  onClose,
  onSaved,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const [keep, setKeep] = useState(initialKeep);
  const [isSaving, setIsSaving] = useState(false);

  const save = async () => {
    setIsSaving(true);
    try {
      const policies = await projectApi.getRetentionPolicies(projectId);
      const logs = policies.find((p) => p.signal === 'logs');
      if (!logs) throw new Error('This project has no log retention settings');
      const rule: LogSampleRule = {
        service: target.service,
        level: target.level,
        pattern: target.hash,
        patternText: target.patternText.slice(0, 500),
        rate: keep / 100,
      };
      await projectApi.setRetentionPolicies(projectId, [
        { ...logs, sampleRules: withSampleRule(logs.sampleRules ?? [], rule) },
      ]);
      message.success('Sampling rule added to Retention & sampling');
      onSaved(rule);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to add the sampling rule');
    } finally {
      setIsSaving(false);
    }
  };

  const preview =
    target.perDay === null ? null : samplingPreview(target.perDay, keep, bytesPerLine);

  return (
    <Modal
      open
      title="Sampling rule"
      onCancel={onClose}
      footer={
        <span className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={isSaving} onClick={save}>
            Add rule
          </Button>
        </span>
      }
    >
      <div className="flex flex-col gap-3">
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Added to Project settings › Retention &amp; sampling › Log sampling rules. Applies to new
          lines only.
        </span>
        <div className="flex flex-col gap-1">
          <span className="text-xs text-gray-500 dark:text-gray-400">Matches</span>
          <span className="rounded-lg border border-gray-200 bg-gray-50 px-2.5 py-1.5 font-mono text-xs break-all text-gray-700 dark:border-gray-700 dark:bg-gray-900/60 dark:text-gray-200">
            service = {target.service || 'any'} · level = {target.level || 'any'} · pattern ={' '}
            {target.patternText || target.hash}
          </span>
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs text-gray-500 dark:text-gray-400">Keep</span>
          <SegmentedControl
            radio
            label="Keep"
            value={keep}
            options={KEEP_CHOICES.map((k) => ({ value: k, label: k === 0 ? 'Drop' : `${k}%` }))}
            onChange={setKeep}
          />
        </div>
        <div className="grid grid-cols-2 gap-2.5">
          <span className="flex flex-col gap-1 rounded-xl border border-gray-200 p-2.5 dark:border-gray-700">
            <span className="text-xs text-gray-500 dark:text-gray-400">Lines kept per day</span>
            <strong className="text-lg font-semibold text-gray-900 dark:text-gray-50">
              {preview ? formatCount(preview.keptPerDay) : 'n/a'}
            </strong>
          </span>
          <span className="flex flex-col gap-1 rounded-xl border border-gray-200 p-2.5 dark:border-gray-700">
            <span className="text-xs text-gray-500 dark:text-gray-400">
              Storage saved per month
            </span>
            <strong className="text-lg font-semibold text-[var(--v2-green,#10b981)]">
              {preview ? formatBytes(preview.savedBytesPerMonth) : 'n/a'}
            </strong>
          </span>
        </div>
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Estimated from the last 7 days and the project&apos;s average stored line size.
          Sampled-out lines are not stored, so searches and charts count only the kept lines.
        </span>
      </div>
    </Modal>
  );
};
