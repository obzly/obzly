import { DeleteOutlined, EditOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, App, Button, Popconfirm, Spin, Tooltip } from 'antd';
import React, { useEffect, useState } from 'react';

import {
  type AiBudget,
  type AiBudgetRequest,
  type AiBudgetStatus,
  aiBudgetsApi,
} from '../../../entity/aiUsage';
import { type NotificationChannel, alertingApi } from '../../../entity/alerting';
import { type StatusColors, useTheme } from '../../../shared/theme';
import { Card, Gauge } from '../../../shared/ui/v2';
import {
  BUDGET_STATE_LABELS,
  budgetState,
  forecastLabel,
  isPerGroup,
  periodLabel,
  progressPercent,
  resetLabel,
  scopeLabel,
  spentLabel,
} from '../lib/budgets';
import type { PillTone } from '../lib/describeEvent';
import { formatUsd } from '../lib/format';
import { BudgetEditorDrawer } from './BudgetEditorDrawer';
import { Pill } from './bits';

interface Props {
  projectId: string;
  canManage: boolean;
}

const errorText = (error: unknown, fallback: string) =>
  error instanceof Error ? error.message : fallback;

const topTitle = (budget: AiBudget): string =>
  budget.scope === 'each_user' ? 'Top users' : 'Top models';

const STATE_TONES = { ok: 'ok', warning: 'warn', over: 'bad' } as const satisfies Record<
  string,
  PillTone
>;

// Status colours for each state: on track stays green in every theme.
const stateColor = (colors: StatusColors, state: keyof typeof STATE_TONES): string =>
  ({ ok: colors.ok, warning: colors.warn, over: colors.bad })[state];

const BudgetCard = ({
  budget,
  status,
  channels,
  canManage,
  onEdit,
  onDelete,
}: {
  budget: AiBudget;
  status?: AiBudgetStatus;
  channels: NotificationChannel[];
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) => {
  const { status: statusColors } = useTheme();
  // No status yet (or it failed) is unknown, not 0% / on track.
  const percent = status?.percent ?? null;
  const state = percent === null ? null : budgetState(percent, budget.thresholds);
  const color = state ? stateColor(statusColors, state) : 'var(--v2-track)';
  const firstThreshold = Math.min(...budget.thresholds, 100);
  const channelNames = budget.channelIds
    .map((id) => channels.find((channel) => channel.id === id)?.name)
    .filter(Boolean);
  const showTop = !!status && status.top.length > 0 && budget.scope !== 'model';

  return (
    <Card
      title={<span className="block truncate text-base">{budget.name}</span>}
      subtitle={scopeLabel(budget)}
      extra={
        canManage && (
          <div className="flex gap-1">
            <Tooltip title="Edit">
              <Button
                type="text"
                size="small"
                icon={<EditOutlined />}
                aria-label="Edit budget"
                onClick={onEdit}
              />
            </Tooltip>
            <Popconfirm
              title={`Delete ${budget.name}?`}
              description="Its alert rule is removed too."
              okText="Delete"
              okButtonProps={{ danger: true }}
              onConfirm={onDelete}
            >
              <Tooltip title="Delete">
                <Button
                  type="text"
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  aria-label="Delete budget"
                />
              </Tooltip>
            </Popconfirm>
          </div>
        )
      }
      bodyClassName="flex flex-col gap-3"
    >
      {status?.error ? (
        <Alert type="warning" showIcon title={status.error} />
      ) : (
        <>
          <div className="flex items-center gap-4">
            <Gauge
              size={88}
              ratio={percent === null ? null : progressPercent(percent) / 100}
              value={
                <span className="text-base">
                  {percent === null ? 'n/a' : `${percent.toFixed(0)}%`}
                </span>
              }
              color={color}
              label={
                percent === null
                  ? 'Budget spend not known yet'
                  : `${percent.toFixed(0)}% of the budget spent`
              }
            />
            <div className="flex min-w-0 flex-col gap-1">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className="text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-50">
                  {status ? spentLabel(budget, status) : 'n/a'}
                </span>
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  of {formatUsd(budget.amountUsd)} {isPerGroup(budget.scope) ? 'each, ' : ''}
                  {periodLabel(budget.period)}
                </span>
              </span>
              {state ? (
                <Pill tone={STATE_TONES[state]} className="self-start">
                  {BUDGET_STATE_LABELS[state]}
                </Pill>
              ) : (
                <Pill tone="muted" className="self-start">
                  no data
                </Pill>
              )}
            </div>
          </div>
          <div
            role="img"
            aria-label={
              percent === null
                ? 'Budget spend not known yet'
                : `${percent.toFixed(0)}% of the budget spent`
            }
            className="relative h-2 rounded-full bg-[var(--v2-track)]"
          >
            <div
              className="h-2 rounded-full"
              style={{ width: `${progressPercent(percent ?? 0)}%`, background: color }}
            />
            {firstThreshold < 100 && (
              <div
                className="absolute -top-[3px] h-3.5 w-0.5 bg-gray-900 opacity-60 dark:bg-gray-50"
                style={{ left: `${firstThreshold}%` }}
              />
            )}
          </div>
          {status && (
            <div className="flex flex-wrap justify-between gap-2 text-xs">
              <span className="text-gray-700 dark:text-gray-200">
                {forecastLabel(budget, status)}
              </span>
              <span className="text-gray-500 dark:text-gray-400">
                {resetLabel(status, budget.timeZone)}
              </span>
            </div>
          )}
          {showTop && status && (
            <div className="flex flex-col gap-1.5">
              <span className="text-xs text-gray-500 dark:text-gray-400">{topTitle(budget)}</span>
              {status.top.map((group) => {
                const groupColor = isPerGroup(budget.scope)
                  ? stateColor(statusColors, budgetState(group.percent, budget.thresholds))
                  : 'var(--v2-blue)';
                return (
                  <div key={group.key} className="flex flex-col gap-0.5 text-xs">
                    <span className="flex justify-between gap-2">
                      <span className="truncate text-gray-800 dark:text-gray-100">{group.key}</span>
                      <span className="shrink-0 font-mono text-gray-700 dark:text-gray-200">
                        {formatUsd(group.spentUsd)}
                        {isPerGroup(budget.scope) ? ` (${group.percent.toFixed(0)}%)` : ''}
                      </span>
                    </span>
                    <span className="block h-1 rounded-full bg-[var(--v2-track)]">
                      <span
                        className="block h-1 rounded-full"
                        style={{
                          width: `${progressPercent(group.percent)}%`,
                          background: groupColor,
                        }}
                      />
                    </span>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
      <div className="border-t border-gray-200 pt-2 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
        {channelNames.length ? `Notifies ${channelNames.join(', ')}` : 'No notification channels'}
        {' · '}
        alerts at {budget.thresholds.map((t) => `${t}%`).join(', ')}
      </div>
    </Card>
  );
};

export const BudgetsComponent = ({ projectId, canManage }: Props): React.JSX.Element => {
  const { message } = App.useApp();

  const [budgets, setBudgets] = useState<AiBudget[]>([]);
  const [statuses, setStatuses] = useState<AiBudgetStatus[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<AiBudget | 'new' | null>(null);

  const load = async () => {
    try {
      const [loadedBudgets, loadedChannels] = await Promise.all([
        aiBudgetsApi.listBudgets(projectId),
        alertingApi.listChannels(projectId),
      ]);
      setBudgets(loadedBudgets);
      setChannels(loadedChannels);
      setLoadError(null);
      setStatuses(loadedBudgets.length ? await aiBudgetsApi.getStatus(projectId) : []);
    } catch (error: unknown) {
      setLoadError(errorText(error, 'Failed to load budgets'));
    } finally {
      setIsLoading(false);
    }
  };

  const save = async (request: AiBudgetRequest) => {
    if (editing && editing !== 'new') {
      await aiBudgetsApi.updateBudget(projectId, editing.id, request);
      message.success('Budget saved');
    } else {
      await aiBudgetsApi.createBudget(projectId, request);
      message.success('Budget created');
    }
    setEditing(null);
    await load();
  };

  const remove = async (budget: AiBudget) => {
    try {
      await aiBudgetsApi.deleteBudget(projectId, budget.id);
      message.success('Budget deleted');
      await load();
    } catch (error: unknown) {
      message.error(errorText(error, 'Failed to delete budget'));
    }
  };

  useEffect(() => {
    setIsLoading(true);
    void load();
  }, [projectId]);

  return (
    <div className="flex flex-col gap-4">
      <Alert
        type="info"
        title="Budgets alert; they can't stop spending. Spend uses the same estimates as the Claude Code and LLM apps tabs, checked every 5 minutes."
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="m-0 text-[17px] font-semibold text-gray-900 dark:text-gray-50">Budgets</h2>
        {canManage && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setEditing('new')}
            className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
          >
            New budget
          </Button>
        )}
      </div>

      {loadError && <Alert type="error" showIcon title={loadError} />}

      {isLoading ? (
        <div className="flex justify-center p-12">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : budgets.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-gray-300 bg-white p-8 text-center text-sm text-gray-500 dark:border-gray-600 dark:bg-gray-800 dark:text-gray-400">
          <strong className="font-semibold text-gray-900 dark:text-gray-50">No budgets yet.</strong>{' '}
          {canManage
            ? 'Set a monthly limit for the project, or a daily one for each user.'
            : 'Project owners and admins can add them.'}
        </div>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(min(380px,100%),1fr))] gap-4">
          {budgets.map((budget) => (
            <BudgetCard
              key={budget.id}
              budget={budget}
              status={statuses.find((status) => status.budgetId === budget.id)}
              channels={channels}
              canManage={canManage}
              onEdit={() => setEditing(budget)}
              onDelete={() => remove(budget)}
            />
          ))}
        </div>
      )}

      {editing && (
        <BudgetEditorDrawer
          budget={editing === 'new' ? null : editing}
          channels={channels}
          onClose={() => setEditing(null)}
          onSave={save}
        />
      )}
    </div>
  );
};
