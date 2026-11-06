import { Alert, App, Button, Checkbox, Drawer, Input, InputNumber, Select } from 'antd';
import React, { useMemo, useState } from 'react';

import type { AiBudget, AiBudgetRequest, BudgetPeriod } from '../../../entity/aiUsage';
import type { NotificationChannel } from '../../../entity/alerting';
import { getAvailableTimezones } from '../../../shared/time';
import { SCOPE_OPTIONS, emptyBudgetRequest, toBudgetRequest } from '../lib/budgets';

interface Props {
  budget: AiBudget | null;
  channels: NotificationChannel[];
  onClose: () => void;
  onSave: (request: AiBudgetRequest) => Promise<void>;
}

const fieldLabel = (text: string, help?: string) => (
  <div className="mb-1">
    <span className="font-medium text-gray-900 dark:text-gray-100">{text}</span>
    {help && <div className="text-xs text-gray-500 dark:text-gray-300">{help}</div>}
  </div>
);

const splitThresholds = (
  thresholds: number[],
): { warn: number | null; critical: number | null } => {
  const below = thresholds.filter((t) => t < 100);
  const atOrAbove = thresholds.filter((t) => t >= 100);
  return {
    warn: below.length ? Math.min(...below) : null,
    critical: atOrAbove.length ? Math.min(...atOrAbove) : null,
  };
};

export const BudgetEditorDrawer = ({
  budget,
  channels,
  onClose,
  onSave,
}: Props): React.JSX.Element => {
  const { modal } = App.useApp();
  // A channel deleted since the budget was saved is dropped, or saving would fail.
  const initial = useMemo(() => {
    const request = budget ? toBudgetRequest(budget) : emptyBudgetRequest();
    return {
      ...request,
      channelIds: request.channelIds.filter((id) => channels.some((c) => c.id === id)),
    };
  }, [budget, channels]);

  const [draft, setDraft] = useState<AiBudgetRequest>(initial);
  const [warn, setWarn] = useState<number | null>(splitThresholds(initial.thresholds).warn);
  const [critical, setCritical] = useState<number | null>(
    splitThresholds(initial.thresholds).critical,
  );
  const [isSaving, setIsSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (patch: Partial<AiBudgetRequest>) => setDraft((prev) => ({ ...prev, ...patch }));

  const thresholds = [warn, critical].filter((t): t is number => t !== null && t > 0);
  const isDirty = JSON.stringify({ ...draft, thresholds }) !== JSON.stringify(initial);

  const close = () => {
    if (!isDirty) {
      onClose();
      return;
    }
    modal.confirm({
      title: 'Discard changes?',
      content: 'The budget has unsaved changes.',
      okText: 'Discard',
      okButtonProps: { danger: true },
      cancelText: 'Keep editing',
      onOk: onClose,
    });
  };

  const save = async () => {
    setIsSaving(true);
    setError(null);
    try {
      await onSave({
        ...draft,
        name: draft.name.trim(),
        scopeValue:
          draft.scope === 'user' || draft.scope === 'model' ? draft.scopeValue.trim() : '',
        thresholds,
      });
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save budget');
    } finally {
      setIsSaving(false);
    }
  };

  const timeZones = useMemo(() => {
    const zones = getAvailableTimezones();
    return (zones.includes('UTC') ? zones : ['UTC', ...zones]).map((zone) => ({
      value: zone,
      label: zone,
    }));
  }, []);
  const needsValue = draft.scope === 'user' || draft.scope === 'model';
  const canSave =
    !!draft.name.trim() &&
    draft.amountUsd > 0 &&
    thresholds.length > 0 &&
    (draft.includeClaudeCode || draft.includeLlmApps) &&
    (!needsValue || !!draft.scopeValue.trim());

  return (
    <Drawer
      open
      width={520}
      title={budget ? `Edit ${budget.name}` : 'New budget'}
      onClose={close}
      extra={
        <Button
          type="primary"
          onClick={save}
          loading={isSaving}
          disabled={!canSave}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          Save budget
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        {error && <Alert type="error" showIcon title={error} />}
        <div>
          {fieldLabel('Name')}
          <Input
            value={draft.name}
            maxLength={200}
            placeholder="Daily per engineer"
            onChange={(e) => set({ name: e.target.value })}
          />
        </div>
        <div className="grid grid-cols-3 gap-3">
          <div>
            {fieldLabel('Amount (USD)')}
            <InputNumber
              className="!w-full"
              min={0.01}
              value={draft.amountUsd}
              onChange={(value) => set({ amountUsd: value ?? 0 })}
            />
          </div>
          <div>
            {fieldLabel('Period')}
            <Select<BudgetPeriod>
              className="!w-full"
              value={draft.period}
              onChange={(period) => set({ period })}
              options={[
                { value: 'day', label: 'Day' },
                { value: 'week', label: 'Week (from Monday)' },
                { value: 'month', label: 'Month' },
              ]}
            />
          </div>
          <div>
            {fieldLabel('Time zone')}
            <Select
              className="!w-full"
              showSearch
              value={draft.timeZone}
              onChange={(timeZone) => set({ timeZone })}
              options={timeZones}
            />
          </div>
        </div>
        <fieldset>
          <legend className="mb-1 font-medium text-gray-900 dark:text-gray-100">Applies to</legend>
          <div className="flex flex-col gap-2">
            {SCOPE_OPTIONS.map((option) => {
              const selected = draft.scope === option.value;
              return (
                <button
                  key={option.value}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => set({ scope: option.value })}
                  className={`flex flex-col rounded-md border px-3 py-2 text-left ${
                    selected
                      ? 'border-emerald-600 bg-emerald-50 dark:bg-emerald-950'
                      : 'border-gray-300 bg-white dark:border-gray-600 dark:bg-gray-800'
                  }`}
                >
                  <span className="font-semibold text-gray-900 dark:text-gray-100">
                    {option.label}
                  </span>
                  <span className="text-xs text-gray-500 dark:text-gray-400">{option.hint}</span>
                </button>
              );
            })}
          </div>
          {needsValue && (
            <Input
              className="!mt-2"
              value={draft.scopeValue}
              placeholder={draft.scope === 'user' ? 'a.rivera@example.com' : 'claude-opus-4-1'}
              onChange={(e) => set({ scopeValue: e.target.value })}
            />
          )}
        </fieldset>
        <fieldset>
          <legend className="mb-1 font-medium text-gray-900 dark:text-gray-100">Counts</legend>
          <div className="flex flex-wrap gap-4">
            <Checkbox
              checked={draft.includeClaudeCode}
              onChange={(e) => set({ includeClaudeCode: e.target.checked })}
            >
              Claude Code
            </Checkbox>
            <Checkbox
              checked={draft.includeLlmApps}
              onChange={(e) => set({ includeLlmApps: e.target.checked })}
            >
              LLM apps (models with a price)
            </Checkbox>
          </div>
        </fieldset>
        <div className="grid grid-cols-2 gap-3">
          <div>
            {fieldLabel('Warn at (%)')}
            <InputNumber
              className="!w-full"
              min={1}
              max={99}
              value={warn}
              placeholder="none"
              onChange={(value) => setWarn(value ?? null)}
            />
          </div>
          <div>
            {fieldLabel('Critical at (%)')}
            <InputNumber
              className="!w-full"
              min={100}
              max={1000}
              value={critical}
              placeholder="none"
              onChange={(value) => setCritical(value ?? null)}
            />
          </div>
        </div>
        <div>
          {fieldLabel('Notify')}
          <Select
            mode="multiple"
            className="!w-full"
            value={draft.channelIds}
            onChange={(channelIds) => set({ channelIds })}
            placeholder={
              channels.length === 0 ? 'No shared channels yet (Obzly settings)' : 'Channels'
            }
            options={channels.map((c) => ({ value: c.id, label: `${c.name} (${c.kind})` }))}
          />
        </div>
        <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
          Each threshold notifies once per period
          {draft.scope === 'each_user'
            ? ' per user'
            : draft.scope === 'each_model'
              ? ' per model'
              : ''}
          , and resolves when the period resets. The rule appears in Alerts with a Budget tag.
          Budgets alert; they can&apos;t stop Claude Code or an app from spending.
        </p>
      </div>
    </Drawer>
  );
};
